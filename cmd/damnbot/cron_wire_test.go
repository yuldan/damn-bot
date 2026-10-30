package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

func openCronDB(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	return db
}

func cronRowCount(t *testing.T, db *ctxdb.Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(query, args, &n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCronOutboxTakesOneDeliveryPerSlot: a retried Tick and a restarted
// adapter enqueue the same slot once; the stored key is per (platform, chat).
func TestCronOutboxTakesOneDeliveryPerSlot(t *testing.T) {
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:7788")
	db := openCronDB(t)
	m := &cron.Delivery{
		JobID: "job-1", ScheduledAt: time.UnixMilli(1000), Outcome: cron.OutcomeOK,
		Output: "the report", IdemKey: "cron:job-1:1000:1",
	}
	for range 2 {
		out := &cronOutbox{store: gwstore.Store{DB: db}, offline: func() bool { return false }}
		if err := out.Deliver(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	}
	key := gateway.DeliveryKey("telegram", "7788", m.IdemKey)
	if n := cronRowCount(t, db, `SELECT count(*) FROM gateway_deliveries WHERE idem_key = ?1`, key); n != 1 {
		t.Fatalf("delivery rows = %d, want 1", n)
	}
	var body string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT body FROM gateway_deliveries WHERE idem_key = ?1`, []any{key}, &body)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "the report") || !strings.Contains(body, "job-1") {
		t.Fatalf("body = %q, want job and output", body)
	}
}

// TestCronOutboxSkipsWithoutDestination: offline and a missing owner both queue
// nothing and report ErrOffline so the scheduler records the slot as skipped.
func TestCronOutboxSkipsWithoutDestination(t *testing.T) {
	for _, tc := range []struct {
		name, owner string
		offline     bool
	}{
		{"offline", "telegram:7788", true},
		{"no owner", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DAMNBOT_GATEWAY_OWNER", tc.owner)
			db := openCronDB(t)
			out := &cronOutbox{store: gwstore.Store{DB: db}, offline: func() bool { return tc.offline }}
			err := out.Deliver(t.Context(), &cron.Delivery{JobID: "j", Output: "x", IdemKey: "cron:j:1:1"})
			if !errors.Is(err, cron.ErrOffline) {
				t.Fatalf("delivery = %v, want ErrOffline", err)
			}
			if n := cronRowCount(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 0 {
				t.Fatalf("queued %d rows, want 0", n)
			}
		})
	}
}

// TestCronAlarmKeyIsHourly: two alarms in one hour collapse on one row, and the
// body is readable.
func TestCronAlarmKeyIsHourly(t *testing.T) {
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:7788")
	db := openCronDB(t)
	out := &cronOutbox{store: gwstore.Store{DB: db}, offline: func() bool { return false }}
	base := time.Date(2026, 10, 30, 10, 2, 0, 0, time.UTC)
	for _, at := range []time.Time{base, base.Add(17 * time.Minute)} {
		if err := out.Alert(t.Context(), &cron.Alarm{
			JobID: "job-1", JobName: "quarter", Kind: cron.IncidentFailed,
			Err: "boom", Failures: 4, At: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n := cronRowCount(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 1 {
		t.Fatalf("alarm rows = %d, want 1 per hour", n)
	}
	var body string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT body FROM gateway_deliveries`, nil, &body)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"quarter"`) || !strings.Contains(body, "boom") {
		t.Fatalf("alarm body = %q, want job name and error", body)
	}
}

func TestCronIncidentRoundTrip(t *testing.T) {
	db := openCronDB(t)
	st := &cronIncidents{db: db}
	if inc, err := st.Incident(t.Context(), "job-1"); err != nil || inc.Failures != 0 {
		t.Fatalf("empty incident = %+v, %v", inc, err)
	}
	want := cron.Incident{Failures: 3, Kind: cron.IncidentFailed, Err: "boom", LastAlarm: time.UnixMilli(1000)}
	if err := st.SaveIncident(t.Context(), "job-1", want); err != nil {
		t.Fatal(err)
	}
	got, err := st.Incident(t.Context(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Failures != want.Failures || got.Kind != want.Kind || got.Err != want.Err || !got.LastAlarm.Equal(want.LastAlarm) {
		t.Fatalf("incident = %+v, want %+v", got, want)
	}
	if n := cronRowCount(t, db, `SELECT count(*) FROM module_state WHERE module_id = 'cron'`); n != 1 {
		t.Fatalf("module_state rows = %d, want 1", n)
	}
}

// TestCronStoreFinishRecordsIncident: stop_reason carries the delivery result
// and the incident kind on the claimed slot (no migration).
func TestCronStoreFinishRecordsIncident(t *testing.T) {
	db := openCronDB(t)
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, err := tx.Exec(`INSERT INTO cron_jobs(id, name, schedule, prompt, prompt_sha, cwd, enabled, created_at, updated_at)
			VALUES ('job-1', 'quarter', '*/15 * * * *', 'p', 'sha-1', '/w', 1, 1000, 1000)`)
		if err != nil {
			return fmt.Errorf("insert cron job: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store := newCronAdapter(db)
	at := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	if ok, err := store.Claim(t.Context(), "job-1", at, at, "serve"); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	run := &cron.Run{
		Outcome: cron.OutcomeFailed, Err: "preflight: disk low-water", Exit: 1, Attempt: 1,
		StartedAt: at, FinishedAt: at.Add(2 * time.Second), Incident: cron.IncidentPreflight,
	}
	if err := store.Finish(t.Context(), "job-1", at, run); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT stop_reason FROM cron_runs WHERE job_id = 'job-1'`, nil, &reason)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "incident=preflight") || !strings.Contains(reason, "duration_ms=2000") {
		t.Fatalf("stop_reason = %q, want incident and duration", reason)
	}
}

func TestCronJobOf(t *testing.T) {
	key := gateway.DeliveryKey("telegram", "1", fmt.Sprintf("cron:%s:%d:1", "job-1", 99))
	if got := cronJobOf(key); got != "job-1" {
		t.Fatalf("job = %q, want job-1", got)
	}
	for _, other := range []string{
		gateway.DeliveryKey("telegram", "1", "cron:alarm:job-1:99"),
		gateway.DeliveryKey("telegram", "1", "final:42"),
		"not-a-key",
	} {
		if got := cronJobOf(other); got != "" {
			t.Fatalf("job of %q = %q, want empty", other, got)
		}
	}
}

// TestServeCronAssembly: the wiring builds a scheduler with a store, executor,
// deliverer and incident sink; with no jobs a tick does not touch the outbox.
func TestServeCronAssembly(t *testing.T) {
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:7788")
	db := openCronDB(t)
	c := newServeCron(t.Context(), db, t.TempDir(), "", false, io.Discard)
	if c.sched.Store == nil || c.sched.Executor == nil || c.sched.Deliver == nil ||
		c.sched.Incidents == nil || c.sched.Alert == nil || c.sched.Preflight == nil {
		t.Fatalf("scheduler wiring incomplete: %+v", c.sched)
	}
	if err := c.tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := cronRowCount(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 0 {
		t.Fatalf("empty scheduler queued %d deliveries", n)
	}
}
