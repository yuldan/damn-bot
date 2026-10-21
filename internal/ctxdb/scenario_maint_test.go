package ctxdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
	"modernc.org/sqlite"
)

func maintScenarios() []scenario {
	return []scenario{
		{name: "S016_RestoreWhileServeAndTUIRun", fn: s016},
		{name: "S017_AutoRestoreBlockedWriterTimeout", fn: s017, serial: true},
		{name: "S018_RestoreClosesOpenJournals", fn: s018},
		{name: "S019_RestoreBeforePurgeReapplies", fn: s019},
		{name: "S020_SalvageModeRestoreGuards", fn: s020},
		{name: "S026_RestorerKilledMarkerStale", fn: s026, serial: true},
		{name: "S028_MaintModeFailClosedAndAbort", fn: s028},
		{name: "S031_RestoreCancelsRunningTurn", fn: s031},
		{name: "S035_TwoRestoresConcurrent", fn: s035, serial: true},
		{name: "S036_PurgePausesForBarrier", fn: s036, serial: true},
		{name: "S037_SigstopInsideWriteTx", fn: s037, serial: true},
		{name: "S042_ForeignDataDirPurgesLabelled", fn: s042},
		{name: "S045_ReapplyPhase0Bounded", fn: s045},
		{name: "S049_MigrationFencesOldBinary", fn: s049},
		{name: "S051_SigtstpReleasesLocks", fn: s051, serial: true},
		{name: "S057_OldBinaryOpensDuringMigration", fn: s057},
		{name: "S058_UpgradeMigratesWhileOldServeRuns", fn: s058},
		{name: "S063_DbWriteStalledReport", fn: s063, serial: true},
		{name: "S067_OldServeRestartedSchemaNewer", fn: s067},
		{name: "S071_MigrationRefusedKeepsOldServe", fn: s071},
		{name: "S078_ServeMigratorForcesAfter10Min", fn: s078},
		{name: "S086_MigratorRestartsAccumulate", fn: s086},
	}
}

func backupDB(t *testing.T, s *Store) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.db")
	must(t, s.Write(context.Background(), func(*Tx) error { return nil })) // ensure attached
	_, err := s.w.ExecContext(context.Background(), "VACUUM INTO ?", p)
	must(t, err)
	return p
}

// restore runs Restore as a separate maintenance process (pid 9).
func (e *env) restore(t *testing.T, backup string) error {
	t.Helper()
	o := e.opts(9, "cli")
	o.BarrierWait = 10 * time.Second
	return Restore(context.Background(), &o, backup)
}

// #16: restore while serve and TUI run: both drain, reopen, reacquire.
func s016(t *testing.T) {
	e := newEnv(t)
	srv, tui := e.open(1, "serve"), e.open(2, "tui")
	session(t, tui, "S", "")
	appendMsg(t, tui, "S", "before backup")
	bk := backupDB(t, tui)
	appendMsg(t, tui, "S", "after backup")
	must(t, e.restore(t, bk))
	_, err := tui.Append(context.Background(), "S", nil, Event{Kind: "user_message"})
	wantErr(t, err, ErrLeaseFenced) // leases of the snapshot are void; token dropped
	_, err = tui.AcquireLease(context.Background(), "S")
	must(t, err)
	appendMsg(t, tui, "S", "after restore")
	if n := count(t, srv, "SELECT count(*) FROM events WHERE session_id = 'S'"); n != 2 {
		t.Fatalf("events = %d, want backup(1) + 1", n)
	}
	checkIntegrity(t, srv)
}

// #17: a process that cannot drain → restore fails ≤ wait, marker removed,
// DB not replaced (SAFE MODE decision belongs to the caller).
func s017(t *testing.T) {
	e := newEnv(t)
	w := e.open(1, "tui")
	session(t, w, "S", "")
	bk := backupDB(t, w)
	release, inTx := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- w.Write(context.Background(), func(tx *Tx) error {
			close(inTx)
			<-release
			_, err := tx.AppendEvent("S", &Event{Kind: "user_message", Content: "kept"})
			return err
		})
	}()
	<-inTx
	o := e.opts(9, "cli")
	o.BarrierWait = time.Second
	wantErr(t, Restore(context.Background(), &o, bk), ErrDBBusy)
	if MaintenancePending(e.dir) {
		t.Fatal("marker left after failed restore")
	}
	close(release)
	must(t, <-done)
	if count(t, w, "SELECT count(*) FROM events WHERE content = 'kept'") != 1 {
		t.Fatal("DB replaced despite failed barrier")
	}
}

// #18: open journals of the snapshot are closed crashed, no auto-continue.
func s018(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	must(t, s.Write(context.Background(), func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	bk := backupDB(t, s)
	must(t, e.restore(t, bk))
	if count(t, s, "SELECT count(*) FROM turn_journal WHERE turn_id = 'T1' AND state = 'crashed'") != 1 {
		t.Fatal("journal not closed crashed")
	}
	if count(t, s, `SELECT count(*) FROM events WHERE kind = 'crash_recovered' AND json_extract(payload, '$.decision') = 'restore'`) != 1 {
		t.Fatal("no crash_recovered{restore}")
	}
}

// #19: restore of a backup taken before a purge → phase 0 reapplied under
// the barrier as one purge; the rest resumes normally; files not revived.
func s019(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S1", "")
	session(t, s, "S2", "")
	pb, err := s.PutBlob([]byte("secret attachment"), "text/plain")
	must(t, err)
	_, err = s.Append(context.Background(), "S1", []*PendingBlob{pb}, Event{Kind: "tool_result", Blob: pb.Sha})
	must(t, err)
	bk := backupDB(t, s)
	must(t, s.ReleaseLease(context.Background(), "S1"))
	_, err = s.Purge(context.Background(), PurgeRequest{IDs: []string{"S1"}})
	must(t, err)
	must(t, e.restore(t, bk))
	wantErr(t, s.Visible(context.Background(), "S1"), ErrNotFound)
	if _, err := os.Stat(s.testBlobPath(pb.Sha)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("purged blob file revived")
	}
	e.procs.kill(9) // the restore command exited
	must(t, s.ResumePurge(context.Background(), false))
	assertNoSessionRefs(t, s, "S1")
	must(t, s.Visible(context.Background(), "S2"))
}

// #20: salvage/restore mode: guard F skipped, guard P and P9 active.
func s020(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	session(t, s, "L", "")
	for _, id := range []string{"S", "L"} {
		must(t, s.ReleaseLease(context.Background(), id))
	}
	purgePhase0(t, s, "L")
	ins := func(sid string) error {
		return s.tx(context.Background(), modeRestore, func(tx *Tx) error { _, err := tx.AppendEvent(sid, &Event{Kind: "user_message"}); return err })
	}
	must(t, ins("S"))                       // no lease needed
	wantErr(t, ins("L"), ErrSessionPurging) // guard P
	err := s.tx(context.Background(), modeRestore, func(tx *Tx) error { _, err := tx.Exec("UPDATE events SET content = 'x'"); return err })
	if err == nil {
		t.Fatal("P9 bypassed in restore mode")
	}
}

// #26: restorer killed after writing the marker → others continue ≤ 10 s.
func s026(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	child := startChild(t, "barrier", e.dir)
	child.kill9(t)
	start := time.Now()
	_, err := s.AcquireLease(context.Background(), "S") // drained during the barrier: tokens dropped, reacquire
	must(t, err)
	appendMsg(t, s, "S", "after restorer death")
	timing.Check(t, "#26 others continue after restorer death", time.Since(start), 10*time.Second)
	if MaintenancePending(e.dir) {
		t.Fatal("stale marker not cleared")
	}
}

// #28: unknown/NULL modes are fail-closed; an aborted purge transaction
// (Goexit stands in for a panic) leaves mode ” on the next transaction.
func s028(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	appendMsg(t, s, "S", "x")
	for _, m := range []string{"gc", "PURGE", "", "restore "} {
		err := s.tx(context.Background(), m, func(tx *Tx) error { _, err := tx.Exec("DELETE FROM events"); return err })
		if err == nil {
			t.Fatalf("mode %q bypassed P9", m)
		}
	}
	if err := nullModeDelete(t, e.dir); err == nil {
		t.Fatal("NULL mode bypassed P9")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := s.tx(context.Background(), modePurge, func(*Tx) error { runtime.Goexit(); return nil })
		t.Errorf("tx survived Goexit: %v", err)
	}()
	<-done
	if m := s.mode.Load(); m != modeNormal {
		t.Fatalf("mode leaked: %q", m)
	}
	err := s.Write(context.Background(), func(tx *Tx) error { _, err := tx.Exec("DELETE FROM events"); return err })
	if err == nil {
		t.Fatal("next transaction ran in purge mode")
	}
}

// nullModeDelete opens the DB through a driver whose dmb_maint() and
// dmb_lease_token() return NULL (a buggy registration) and tries P9 and
// guard-protected writes.
func nullModeDelete(t *testing.T, dir string) error {
	t.Helper()
	d := &sqlite.Driver{}
	null := func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) { return nil, nil }
	must(t, errors.Join(d.RegisterScalarFunction("dmb_maint", 0, null), d.RegisterScalarFunction("dmb_lease_token", 1, null),
		d.RegisterScalarFunction("dmb_fts_norm", 1, func(_ *sqlite.FunctionContext, a []driver.Value) (driver.Value, error) { return a[0], nil })))
	db := sql.OpenDB(connector{d: d, dsn: "file:" + DBPath(dir) + "?_pragma=busy_timeout(2000)&_pragma=foreign_keys(1)"})
	defer func() { must(t, db.Close()) }()
	_, err := db.ExecContext(context.Background(), "DELETE FROM events")
	if err == nil {
		return nil
	}
	_, err2 := db.ExecContext(context.Background(), "INSERT INTO session_labels(session_id, key, value, created_at) VALUES ('S', 'purge_state', 'x', 0)")
	return wrap(err2)
}

// #31: restore during a TUI turn: the turn cannot write after the swap.
func s031(t *testing.T) {
	e := newEnv(t)
	tui := e.open(1, "tui")
	session(t, tui, "S", "")
	bk := backupDB(t, tui)
	must(t, tui.Write(context.Background(), func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	n0 := count(t, tui, "SELECT count(*) FROM events WHERE session_id = 'S'")
	must(t, e.restore(t, bk))
	_, err := tui.Append(context.Background(), "S", nil, Event{Kind: "assistant_message", Content: "late"})
	wantErr(t, err, ErrLeaseFenced)
	if n := count(t, tui, "SELECT count(*) FROM events WHERE session_id = 'S'"); n >= n0 {
		t.Fatalf("events after restore %d (backup had fewer than %d)", n, n0)
	}
}

// #35: two restores at the same time → the second gets db_busy.
func s035(t *testing.T) {
	e := newEnv(t)
	b1, err := AcquireBarrier(context.Background(), e.dir, *e.ident(8), reasonRestore, 30, time.Second)
	must(t, err)
	defer func() { must(t, b1.Release()) }()
	_, err = AcquireBarrier(context.Background(), e.dir, *e.ident(9), reasonRestore, 30, time.Second)
	wantErr(t, err, ErrDBBusy)
}

// #36: purge running when a barrier is requested: finishes the batch,
// releases the purge lease, resumes after the barrier.
func s036(t *testing.T) {
	e := newEnv(t)
	n := 0
	var owner *os.File
	p := e.open(1, "tui", func(o *Options) {
		o.OnTx = func(mode string, _ time.Duration) {
			if mode != modePurge {
				return
			}
			if n++; n == 2 { // request the barrier mid-purge
				owner, _ = testLock(t, e.dir, ownerFile, true)
				must(t, os.WriteFile(runPath(e.dir, "maint.request"), []byte(`{"reason":"restore"}`), 0o600))
			}
		}
	})
	for i := range 5 {
		id := "S" + itoa(int64(i))
		session(t, p, id, "")
		appendMsg(t, p, id, "x")
		must(t, p.ReleaseLease(context.Background(), id))
	}
	e.clk.Advance(time.Second)
	_, err := p.Purge(context.Background(), PurgeRequest{OlderThan: time.Millisecond, WithForks: true})
	wantErr(t, err, ErrDBBusy)
	must(t, os.Remove(runPath(e.dir, "maint.request"))) // barrier done
	must(t, unlockFile(owner))
	var exp int64
	must(t, p.get(context.Background(), "SELECT json_extract(value, '$.expires_at') FROM module_state WHERE module_id = 'purge'", nil, &exp))
	if exp != 0 {
		t.Fatal("purge lease not released before draining")
	}
	must(t, p.ResumePurge(context.Background(), false))
	if count(t, p, "SELECT count(*) FROM sessions WHERE id <> '__system__'") != 0 {
		t.Fatal("purge not finished after barrier")
	}
}

// #37: process SIGSTOPped inside a write transaction: others get the typed
// db_busy after busy_timeout; nothing is killed automatically.
func s037(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui", func(o *Options) { o.BusyTimeout = 300 * time.Millisecond })
	session(t, s, "S", "")
	child := startChild(t, "hold-tx", e.dir)
	child.signal(t, sigStop)
	_, err := s.Append(context.Background(), "S", nil, Event{Kind: "user_message"})
	wantErr(t, err, ErrDBBusy)
	child.signal(t, sigCont)
	child.kill9(t)
	appendMsg(t, s, "S", "after stall")
}

// #42: snapshot taken mid-purge restored into another data dir (no ledger):
// labels are authoritative, P = labelled set, I1 holds.
func s042(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "A", "")
	session(t, s, "B", "A")
	for _, id := range []string{"A", "B"} {
		must(t, s.ReleaseLease(context.Background(), id))
	}
	purgePhase0(t, s, "A")
	bk := backupDB(t, s)
	e2 := newEnv(t)
	must(t, e2.restore(t, bk))
	e2.procs.kill(9)
	s2 := e2.open(2, "tui")
	must(t, s2.ResumePurge(context.Background(), false))
	if count(t, s2, "SELECT count(*) FROM sessions WHERE id IN ('A','B')") != 0 {
		t.Fatal("labelled sessions survived in the foreign data dir")
	}
	checkIntegrity(t, s2)
}

// #45: reapply = mode 'purge' + new purge lease of the barrier owner,
// phase 0 only, released (expires_at = 0) before the barrier is dropped.
func s045(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	bk := backupDB(t, s)
	must(t, s.ReleaseLease(context.Background(), "S"))
	old := purgePhase0(t, s, "S")
	must(t, e.restore(t, bk))
	var raw, label string
	must(t, s.get(context.Background(), "SELECT value FROM module_state WHERE module_id = 'purge'", nil, &raw))
	must(t, s.get(context.Background(), "SELECT value FROM session_labels WHERE session_id = 'S' AND key = 'purge_state' ORDER BY id DESC LIMIT 1", nil, &label))
	var v purgeValue
	must(t, json.Unmarshal([]byte(raw), &v))
	if v.PID != 9 || v.Phase != 1 || v.ExpiresAt != 0 || label == old {
		t.Fatalf("reapply value %+v label %s (old %s)", v, label, old)
	}
}

func (e *env) openOld(pid int, kind string) *Store {
	return e.open(pid, kind, func(o *Options) { o.SchemaMax = 7 })
}

// #49: with --force-migrate the old binary is fenced by user_version.
func s049(t *testing.T) {
	e := newEnv(t)
	old := e.openOld(1, "tui")
	must(t, old.Write(context.Background(), func(tx *Tx) error { _, err := tx.CreateSession(Session{ID: "S"}); return err }))
	e.open(2, "tui", func(o *Options) { o.ForceMigrate = true; o.BarrierWait = 5 * time.Second })
	_, err := old.Append(context.Background(), "S", nil, Event{Kind: "user_message"})
	wantErr(t, err, ErrSchemaNewer)
}

// #51: Ctrl-Z waits for the running transaction, then releases maint.lock.
func s051(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	inTx, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Write(context.Background(), func(tx *Tx) error {
			close(inTx)
			<-release
			_, err := tx.AppendEvent("S", &Event{Kind: "user_message"})
			return err
		})
	}()
	<-inTx
	susp := make(chan struct{})
	go func() { t.Log(s.Suspend()); close(susp) }()
	close(release)
	must(t, <-done)
	<-susp
	f, ok := testLock(t, e.dir, lockFile, true)
	if !ok {
		t.Fatal("maint.lock still held after Suspend")
	}
	must(t, unlockFile(f))
	appendMsg(t, s, "S", "after SIGCONT") // reattach, lease still ours
}

// #57: an old binary starting during a migration blocks on the open order
// and then refuses with schema_newer.
func s057(t *testing.T) {
	e := newEnv(t)
	o0 := e.openOld(1, "tui")
	must(t, o0.Close())
	b, err := AcquireBarrier(context.Background(), e.dir, *e.ident(9), reasonMigrate, 30, time.Second)
	must(t, err)
	res := make(chan error, 1)
	go func() {
		o := e.opts(2, "tui")
		o.SchemaMax = 7
		s, ierr := Open(context.Background(), &o)
		if ierr == nil {
			ierr = s.Close()
		}
		res <- ierr
	}()
	mig, err := newStore(new(e.opts(9, "tui")))
	must(t, err)
	mig.w, mig.r, err = mig.openHandles(context.Background())
	must(t, err)
	must(t, mig.applyMigration(context.Background(), len(mig.migs), mig.migs[len(mig.migs)-1]))
	must(t, errors.Join(mig.detach(), mig.closeRoot()))
	must(t, b.Release())
	wantErr(t, <-res, ErrSchemaNewer)
}

// #58: package upgrade: new TUI migrates while only the old serve runs;
// old serve gets schema_newer (exits 75); new serve drains the queue.
func s058(t *testing.T) {
	e := newEnv(t)
	old := e.openOld(1, "serve")
	for i := range 5 {
		ingest(t, old, inbound("u:"+itoa(int64(i)), "c1", "msg "+itoa(int64(i))))
	}
	e.open(2, "tui", func(o *Options) { o.BarrierWait = 5 * time.Second })
	_, err := old.ProcessInbound(context.Background(), "telegram", "c1")
	wantErr(t, err, ErrSchemaNewer)
	must(t, old.Close())
	e.procs.kill(1)
	srv := e.open(3, "serve")
	for range 5 {
		_, err := srv.ProcessInbound(context.Background(), "telegram", "c1")
		must(t, err)
	}
	if count(t, srv, "SELECT count(*) FROM events WHERE kind = 'user_message'") != 5 {
		t.Fatal("queued inbound lost across upgrade")
	}
}

// #63: db_write_stalled is reported without DB writes; the DB event is
// written after the stall ends.
func s063(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve", func(o *Options) { o.BusyTimeout = 200 * time.Millisecond })
	if count(t, s, `SELECT count(*) FROM events WHERE json_extract(payload, '$.kind') = 'db_write_stalled'`) != 0 {
		t.Fatal("stall event before the stall")
	}
	child := startChild(t, "hold-tx", e.dir)
	child.signal(t, sigStop)
	st := &stallTracker{s: s}
	for range 4 { // 4 × busy, clock advanced 10 s each → ≥ 30 s
		_, err := s.Append(context.Background(), SystemSession, nil, Event{Kind: "system_notice"})
		st.observe(t, err)
		e.clk.Advance(10 * time.Second)
	}
	if _, err := os.Stat(runPath(e.dir, "db_stall.json")); err != nil {
		t.Fatal("no db_stall.json during stall")
	}
	if count(t, s, `SELECT count(*) FROM events WHERE json_extract(payload, '$.kind') = 'db_write_stalled'`) != 0 {
		t.Fatal("DB write during stall")
	}
	child.signal(t, sigCont)
	child.kill9(t)
	must(t, st.end())
	if count(t, s, `SELECT count(*) FROM events WHERE json_extract(payload, '$.kind') = 'db_write_stalled'`) != 1 {
		t.Fatal("stall event not written after the stall")
	}
}

// #67: serve restarted with the old binary after schema_newer → refuses to
// start (exit 78 by caller); queue and spool intact.
func s067(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	ingest(t, s, inbound("u:1", "c1", "queued"))
	must(t, s.SpoolInbound(1, new(inbound("u:2", "c1", "spooled"))))
	must(t, s.Close())
	o := e.opts(2, "serve")
	o.SchemaMax = 7
	_, err := Open(context.Background(), &o)
	wantErr(t, err, ErrSchemaNewer)
	s2 := e.open(3, "serve")
	if count(t, s2, "SELECT count(*) FROM gateway_inbound WHERE event_id IS NULL") != 1 {
		t.Fatal("queue changed")
	}
	if n, err := s2.IngestSpool(context.Background()); err != nil || n != 1 {
		t.Fatal("spool not intact")
	}
}

// #71: an old TUI alive → migration refused without a marker; old serve
// keeps serving the old schema.
func s071(t *testing.T) {
	e := newEnv(t)
	oldSrv, oldTui := e.openOld(1, "serve"), e.openOld(2, "tui")
	_ = oldTui
	o := e.opts(3, "tui")
	_, err := Open(context.Background(), &o)
	wantErr(t, err, ErrMigrationRefused)
	if MaintenancePending(e.dir) {
		t.Fatal("marker written for a refused migration")
	}
	ingest(t, oldSrv, inbound("u:1", "c1", "still served"))
}

// #78: serve as migrator blocked by an old TUI forces after 10 min.
func s078(t *testing.T) {
	e := newEnv(t)
	e.openOld(1, "tui")
	o := e.opts(2, "serve")
	o.BarrierWait = 5 * time.Second
	_, err := Open(context.Background(), &o)
	wantErr(t, err, ErrMigrationRefused)
	e.clk.Advance(10 * time.Minute)
	s, err := Open(context.Background(), &o)
	must(t, err)
	must(t, s.Close())
}

// #86: restarts of the serve migrator accumulate the blocked time across
// processes (run/migrate_blocked.json, boot clock).
func s086(t *testing.T) {
	e := newEnv(t)
	e.openOld(1, "tui")
	for i := range 4 { // each restart waits < 10 min on its own
		o := e.opts(10+i, "serve")
		o.BarrierWait = 5 * time.Second
		_, err := Open(context.Background(), &o)
		wantErr(t, err, ErrMigrationRefused)
		e.clk.Advance(3 * time.Minute)
	}
	o := e.opts(20, "serve")
	o.BarrierWait = 5 * time.Second
	s, err := Open(context.Background(), &o)
	must(t, err)
	must(t, s.Close())
}

// stallTracker is the serve-side reference of `db_write_stalled`
// (§11.11.7): reporting without DB writes during the stall, DB event after.
type stallTracker struct {
	s     *Store
	since int64
}

func (st *stallTracker) observe(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrDBBusy) {
		return
	}
	now := st.s.clk.BootMs()
	if st.since == 0 {
		st.since = now
	}
	if now-st.since >= 30_000 {
		b, err := json.Marshal(map[string]any{"since": st.since, "suspects": st.s.holders()})
		must(t, err)
		must(t, writeAtomic(testRoot(t, st.s.dir), "run/db_stall.json", b))
	}
}

func (st *stallTracker) end() error {
	_, err := st.s.Append(context.Background(), SystemSession, nil, Event{
		Kind:    "system_notice",
		Payload: jsonOf(map[string]any{"kind": "db_write_stalled", "since": st.since, "duration": st.s.clk.BootMs() - st.since}),
	})
	return err
}
