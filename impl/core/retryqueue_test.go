package core

import (
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
