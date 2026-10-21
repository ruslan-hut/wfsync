package wfirma

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"wfsync/entity"
)

// localCopyDB records the writes made to the local wFirma document copy.
// Methods the tests do not reach are left to the nil embedded interface.
type localCopyDB struct {
	Database
	mu      sync.Mutex
	saved   map[string]interface{}
	deleted []string
}

func (d *localCopyDB) SaveInvoice(id string, invoice interface{}) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.saved == nil {
		d.saved = map[string]interface{}{}
	}
	d.saved[id] = invoice
	return nil
}

func (d *localCopyDB) DeleteInvoiceById(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, id)
	return nil
}

func (d *localCopyDB) GetInvoicesByDateRange(string, string, string) ([]*entity.LocalInvoice, error) {
	return nil, nil
}

func newLocalCopyClient(t *testing.T, h http.HandlerFunc) (*Client, *localCopyDB) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	db := &localCopyDB{}
	return &Client{
		enabled: true,
		hc:      srv.Client(),
		baseURL: srv.URL,
		db:      db,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, db
}

// The pull upserts with $set, so it must name only the fields it read: anything else
// would blank the id_external the duplicate guard relies on.
func TestSyncFromRemoteSetsOnlyRemoteFields(t *testing.T) {
	c, db := newLocalCopyClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":{"code":"OK"},"invoices":{"0":{"invoice":{"id":"101","fullnumber":"FV 1/2026","type":"normal","date":"2026-10-15","id_external":""}}}}`)
	})

	if _, err := c.SyncFromRemote(context.Background(), "2026-10-01", "2026-10-31"); err != nil {
		t.Fatal(err)
	}

	got, ok := db.saved["101"].(map[string]interface{})
	if !ok {
		t.Fatalf("saved %T, want a field map", db.saved["101"])
	}
	want := map[string]string{"id": "101", "number": "FV 1/2026", "type": "normal", "date": "2026-10-15"}
	if len(got) != len(want) {
		t.Errorf("saved fields %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %q", k, got[k], v)
		}
	}
}

// A reissued proforma set can take the deleted one's number; the deleted document's local
// copy must go with it, or the number is stored twice.
func TestDeleteProformaForgetsLocalCopy(t *testing.T) {
	tests := []struct {
		name   string
		get    string
		delete bool
	}{
		{"deleted", `{"status":{"code":"OK"},"invoices":{"0":{"invoice":{"id":"608101972","type":"proforma"}}}}`, true},
		{"already gone", `{"status":{"code":"NOT FOUND"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleted := false
			c, db := newLocalCopyClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/invoices/get/"):
					_, _ = io.WriteString(w, tt.get)
				case strings.HasPrefix(r.URL.Path, "/invoices/delete/"):
					deleted = true
					_, _ = io.WriteString(w, `{"status":{"code":"OK"}}`)
				}
			})

			if err := c.DeleteProforma(context.Background(), "608101972"); err != nil {
				t.Fatal(err)
			}
			if deleted != tt.delete {
				t.Errorf("remote delete called = %v, want %v", deleted, tt.delete)
			}
			if len(db.deleted) != 1 || db.deleted[0] != "608101972" {
				t.Errorf("local deletes = %v, want [608101972]", db.deleted)
			}
		})
	}
}

// A document that is not a proforma is refused and its local copy kept.
func TestDeleteProformaKeepsLocalCopyOfFaktura(t *testing.T) {
	c, db := newLocalCopyClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":{"code":"OK"},"invoices":{"0":{"invoice":{"id":"1","type":"normal"}}}}`)
	})

	if err := c.DeleteProforma(context.Background(), "1"); err == nil {
		t.Fatal("expected refusal for a normal invoice")
	}
	if len(db.deleted) != 0 {
		t.Errorf("local deletes = %v, want none", db.deleted)
	}
}
