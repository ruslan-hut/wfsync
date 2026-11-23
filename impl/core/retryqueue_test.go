package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
	"wfsync/entity"
)

// TestPickRetryJob pins the selection rule behind /retry: a completed job wins outright
// (the invoice exists, nothing may be re-issued), otherwise the newest unfinished one.
func TestPickRetryJob(t *testing.T) {
	job := func(id string, status entity.RetryJobStatus) *entity.RetryJob {
		return &entity.RetryJob{ID: id, EventId: id, Status: status, CreatedAt: time.Now()}
	}

	cases := []struct {
		name string
		jobs []*entity.RetryJob
		want string
	}{
		{
			name: "no jobs",
			jobs: nil,
			want: "",
		},
		{
			name: "single failed job",
			jobs: []*entity.RetryJob{job("a", entity.RetryJobFailed)},
			want: "a",
		},
		{
			name: "completed wins over a newer pending one",
			jobs: []*entity.RetryJob{job("new", entity.RetryJobPending), job("old", entity.RetryJobCompleted)},
			want: "old",
		},
		{
			name: "newest unfinished when none completed",
			jobs: []*entity.RetryJob{job("new", entity.RetryJobPending), job("old", entity.RetryJobFailed)},
			want: "new",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickRetryJob(tc.jobs)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("got %q, want nil", got.ID)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want %q", tc.want)
			}
			if got.ID != tc.want {
				t.Errorf("got %q, want %q", got.ID, tc.want)
			}
		})
	}
}

// fakeRetryDB keeps retry jobs in memory; checkout params are served from params.
type fakeRetryDB struct {
	jobs   map[string]*entity.RetryJob
	params *entity.CheckoutParams
}

func (f *fakeRetryDB) SaveRetryJob(job *entity.RetryJob) error {
	f.jobs[job.ID] = job
	return nil
}
func (f *fakeRetryDB) GetPendingRetryJobs() ([]*entity.RetryJob, error) { return nil, nil }
func (f *fakeRetryDB) GetRetryJobsByOrderId(string) ([]*entity.RetryJob, error) {
	return nil, nil
}
func (f *fakeRetryDB) UpdateRetryJob(job *entity.RetryJob) error {
	f.jobs[job.ID] = job
	return nil
}
func (f *fakeRetryDB) GetRetryJobByEventId(id string) (*entity.RetryJob, error) {
	return f.jobs[id], nil
}
func (f *fakeRetryDB) GetCheckoutParamsForEvent(string) (*entity.CheckoutParams, error) {
	return f.params, nil
}

// fakeInvoiceService fails RegisterInvoice with err; other methods are not reached.
type fakeInvoiceService struct {
	InvoiceService
	err error
}

func (f *fakeInvoiceService) RegisterInvoice(context.Context, *entity.CheckoutParams) (*entity.Payment, error) {
	return nil, f.err
}

// TestRetryQueueClassification pins which failures the queue keeps retrying: a rejection of
// the buyer's data is terminal (fixing the order needs a human, then /retry), anything
// else — network, wFirma outage — is rescheduled with backoff as before.
func TestRetryQueueClassification(t *testing.T) {
	customerErr := fmt.Errorf("create contractor: %w",
		&entity.CustomerDataError{Field: "email", Message: "Nieprawidłowy adres e-mail."})
	transientErr := fmt.Errorf("create contractor: %w", errors.New("dial tcp: i/o timeout"))

	cases := []struct {
		name       string
		err        error
		wantStatus entity.RetryJobStatus
		wantError  string
	}{
		{
			name:       "customer data rejected",
			err:        customerErr,
			wantStatus: entity.RetryJobFailed,
			wantError:  "create contractor: wfirma rejected email: Nieprawidłowy adres e-mail.",
		},
		{
			name:       "transient failure",
			err:        transientErr,
			wantStatus: entity.RetryJobPending,
			wantError:  "create contractor: dial tcp: i/o timeout",
		},
	}

	newQueue := func(err error) (*RetryQueue, *fakeRetryDB) {
		rq := NewRetryQueue(slog.New(slog.NewTextHandler(io.Discard, nil)), 5, 5, 60, 0)
		db := &fakeRetryDB{
			jobs:   map[string]*entity.RetryJob{},
			params: &entity.CheckoutParams{EventId: "evt", OrderId: "18360"},
		}
		rq.SetDatabase(db)
		rq.SetInvoiceService(&fakeInvoiceService{err: err})
		return rq, db
	}

	for _, tc := range cases {
		t.Run("enqueue/"+tc.name, func(t *testing.T) {
			rq, db := newQueue(tc.err)
			rq.Enqueue(db.params, tc.err)
			job := db.jobs["evt"]
			if job == nil {
				t.Fatal("no job saved")
			}
			if job.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", job.Status, tc.wantStatus)
			}
			if job.LastError != tc.wantError {
				t.Errorf("last error = %q, want %q", job.LastError, tc.wantError)
			}
			if tc.wantStatus == entity.RetryJobPending && job.NextRetryAt.IsZero() {
				t.Error("pending job has no next retry time")
			}
		})

		t.Run("attempt/"+tc.name, func(t *testing.T) {
			rq, db := newQueue(tc.err)
			job := &entity.RetryJob{ID: "evt", EventId: "evt", OrderId: "18360",
				Status: entity.RetryJobPending, MaxAttempts: 5}
			rq.processOneJob(job, false)
			if job.Attempts != 1 {
				t.Errorf("attempts = %d, want 1", job.Attempts)
			}
			if job.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", job.Status, tc.wantStatus)
			}
			if job.LastError != tc.wantError {
				t.Errorf("last error = %q, want %q", job.LastError, tc.wantError)
			}
			if db.jobs["evt"] != job {
				t.Error("job not persisted")
			}
		})
	}
}
