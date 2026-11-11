package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"wfsync/entity"
)

type fakePaymentTimeStore struct {
	ids     []int64
	listErr error
	written map[int64]time.Time
	failIds map[int64]bool
}

func (f *fakePaymentTimeStore) OrdersMissingPaymentTime(_ time.Time, _ int) ([]int64, error) {
	return f.ids, f.listErr
}

func (f *fakePaymentTimeStore) SetPaymentTimeIfEmpty(orderId int64, at time.Time) error {
	if f.failIds[orderId] {
		return errors.New("write failed")
	}
	if f.written == nil {
		f.written = map[int64]time.Time{}
	}
	f.written[orderId] = at
	return nil
}

type fakeRecordSource struct {
	records map[string]*entity.CheckoutParams
	failIds map[string]bool
}

func (f *fakeRecordSource) GetCheckoutParamsByOrder(orderId string) (*entity.CheckoutParams, error) {
	if f.failIds[orderId] {
		return nil, errors.New("read failed")
	}
	return f.records[orderId], nil
}

// TestPaymentFinalTime pins which local timestamp dates a final payment: closed (set
// right after the outcome) wins, modified is the fallback, nothing yields zero.
func TestPaymentFinalTime(t *testing.T) {
	closed := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		params *entity.CheckoutParams
		want   time.Time
	}{
		{name: "nil record", params: nil, want: time.Time{}},
		{name: "closed wins", params: &entity.CheckoutParams{Closed: closed, Modified: modified}, want: closed},
		{name: "modified when not closed", params: &entity.CheckoutParams{Modified: modified}, want: modified},
		{name: "no times", params: &entity.CheckoutParams{}, want: time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := paymentFinalTime(tc.params); !got.Equal(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBackfillPaymentTime checks that only orders with a datable local record get a
// time and that read/write failures are counted, not fatal.
func TestBackfillPaymentTime(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	closed := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		store       *fakePaymentTimeStore
		src         *fakeRecordSource
		wantFilled  int
		wantMissing int
		wantWritten []int64
		wantErr     bool
	}{
		{
			name:  "nothing to do",
			store: &fakePaymentTimeStore{},
			src:   &fakeRecordSource{},
		},
		{
			name:    "list error is returned",
			store:   &fakePaymentTimeStore{listErr: errors.New("db down")},
			src:     &fakeRecordSource{},
			wantErr: true,
		},
		{
			name:  "mixed outcomes",
			store: &fakePaymentTimeStore{ids: []int64{1, 2, 3, 4, 5}, failIds: map[int64]bool{5: true}},
			src: &fakeRecordSource{
				records: map[string]*entity.CheckoutParams{
					"1": {Closed: closed},
					"3": {}, // record without any time
					"5": {Closed: closed},
				},
				failIds: map[string]bool{"4": true},
			},
			// 1 filled; 2 no record, 3 no time, 4 read error, 5 write error
			wantFilled:  1,
			wantMissing: 4,
			wantWritten: []int64{1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filled, missing, err := backfillPaymentTime(context.Background(), log, tc.store, tc.src, time.Time{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if filled != tc.wantFilled || missing != tc.wantMissing {
				t.Fatalf("filled/missing = %d/%d, want %d/%d", filled, missing, tc.wantFilled, tc.wantMissing)
			}
			if len(tc.store.written) != len(tc.wantWritten) {
				t.Fatalf("written %v, want ids %v", tc.store.written, tc.wantWritten)
			}
			for _, id := range tc.wantWritten {
				if got := tc.store.written[id]; !got.Equal(closed) {
					t.Fatalf("order %s written %v, want %v", strconv.FormatInt(id, 10), got, closed)
				}
			}
		})
	}
}
