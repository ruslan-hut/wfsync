package database

import (
	"database/sql"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// testMySql connects to the MySQL named by WFSYNC_TEST_MYSQL_DSN (a scratch database,
// e.g. "root:pw@tcp(127.0.0.1:3306)/wfsync_test?parseTime=true") and creates a minimal
// order table with the wf_payment_* columns the same way NewSQLClient does. Skipped in
// -short mode and when the variable is unset.
func testMySql(t *testing.T) *MySql {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	dsn := os.Getenv("WFSYNC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("WFSYNC_TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := &MySql{
		db:         db,
		loc:        time.UTC,
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		prefix:     "wftest_",
		structure:  make(map[string]map[string]Column),
		statements: make(map[string]*sql.Stmt),
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP TABLE IF EXISTS wftest_order")
		s.Close()
	})
	if _, err = db.Exec("DROP TABLE IF EXISTS wftest_order"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err = db.Exec(`CREATE TABLE wftest_order (
		order_id INT NOT NULL PRIMARY KEY,
		date_added DATETIME NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, c := range []struct{ name, typ string }{
		{"wf_payment_status", "VARCHAR(32) NOT NULL DEFAULT ''"},
		{"wf_payment_id", "VARCHAR(64) NOT NULL DEFAULT ''"},
		{"wf_payment_amount", "BIGINT NOT NULL DEFAULT 0"},
		{"wf_payment_session", "VARCHAR(128) NOT NULL DEFAULT ''"},
		{"wf_payment_time", "DATETIME NULL DEFAULT NULL"},
	} {
		if err = s.addColumnIfNotExists("order", c.name, c.typ); err != nil {
			t.Fatalf("add column %s: %v", c.name, err)
		}
	}
	// twice: the second call must be a no-op
	for i := 0; i < 2; i++ {
		if err = s.addIndexIfNotExists("order", "idx_wf_payment_time", "wf_payment_time"); err != nil {
			t.Fatalf("add index: %v", err)
		}
	}
	return s
}

func paymentTime(t *testing.T, s *MySql, orderId int64) sql.NullTime {
	t.Helper()
	var at sql.NullTime
	if err := s.db.QueryRow("SELECT wf_payment_time FROM wftest_order WHERE order_id = ?", orderId).Scan(&at); err != nil {
		t.Fatalf("read time: %v", err)
	}
	return at
}

// TestUpdatePaymentTime pins when UpdatePayment stamps wf_payment_time: only on the
// change into paid/canceled, never on intermediate statuses or a repeated outcome.
func TestUpdatePaymentTime(t *testing.T) {
	s := testMySql(t)
	if _, err := s.db.Exec("INSERT INTO wftest_order (order_id, date_added) VALUES (1, UTC_TIMESTAMP())"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	steps := []struct {
		status   string
		wantSet  bool
		wantSame bool // time must equal the one after the previous step
	}{
		{status: "pending", wantSet: false},
		{status: "requires_capture", wantSet: false},
		{status: "paid", wantSet: true},
		{status: "paid", wantSet: true, wantSame: true},
		{status: "canceled", wantSet: true},
	}

	var prev sql.NullTime
	for i, st := range steps {
		if st.status == "canceled" {
			// make the stamp distinguishable from the paid one (second resolution)
			time.Sleep(1100 * time.Millisecond)
		}
		if err := s.UpdatePayment(1, "pi_1", "cs_1", st.status, 1000); err != nil {
			t.Fatalf("step %d %s: %v", i, st.status, err)
		}
		got := paymentTime(t, s, 1)
		if got.Valid != st.wantSet {
			t.Fatalf("step %d %s: time set = %v, want %v", i, st.status, got.Valid, st.wantSet)
		}
		if st.wantSame && !got.Time.Equal(prev.Time) {
			t.Fatalf("step %d %s: time moved from %v to %v", i, st.status, prev.Time, got.Time)
		}
		if st.status == "canceled" && got.Time.Equal(prev.Time) {
			t.Fatalf("step %d %s: time not restamped on paid -> canceled", i, st.status)
		}
		if got.Valid && time.Since(got.Time) > time.Minute {
			t.Fatalf("step %d %s: time %v is not UTC now", i, st.status, got.Time)
		}
		prev = got
	}
}

// TestPaymentTimeBackfillQueries checks the backfill selection and that an existing
// time is never overwritten.
func TestPaymentTimeBackfillQueries(t *testing.T) {
	s := testMySql(t)
	now := time.Now().UTC()
	rows := []struct {
		id     int64
		status string
		added  time.Time
		at     any
	}{
		{1, "paid", now, nil},                       // selected
		{2, "canceled", now, nil},                   // selected
		{3, "requires_capture", now, nil},           // not final
		{4, "paid", now.AddDate(0, 0, -200), nil},   // too old
		{5, "paid", now, now.Format(time.DateTime)}, // already has a time
	}
	for _, r := range rows {
		if _, err := s.db.Exec(
			"INSERT INTO wftest_order (order_id, date_added, wf_payment_status, wf_payment_time) VALUES (?, ?, ?, ?)",
			r.id, r.added.Format(time.DateTime), r.status, r.at); err != nil {
			t.Fatalf("insert %d: %v", r.id, err)
		}
	}

	ids, err := s.OrdersMissingPaymentTime(now.AddDate(0, 0, -90), 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("ids = %v, want [1 2]", ids)
	}

	at := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if err = s.SetPaymentTimeIfEmpty(1, at); err != nil {
		t.Fatalf("set 1: %v", err)
	}
	if got := paymentTime(t, s, 1); !got.Valid || !got.Time.Equal(at) {
		t.Fatalf("order 1 time = %v, want %v", got, at)
	}
	before := paymentTime(t, s, 5)
	if err = s.SetPaymentTimeIfEmpty(5, at); err != nil {
		t.Fatalf("set 5: %v", err)
	}
	if got := paymentTime(t, s, 5); !got.Time.Equal(before.Time) {
		t.Fatalf("order 5 time overwritten: %v -> %v", before.Time, got.Time)
	}
}
