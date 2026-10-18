package ctxdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func purgeScenarios() []scenario {
	return []scenario{
		{"S002_StaleHolderWakesAfterLabel", s002},
		{"S006_RecoverySkipsPurgingSession", s006},
		{"S007_CrashAtEveryAppendStep", s007},
		{"S008_ForkFromLabelledOrDuringPhase0", s008},
		{"S009_UndoInPurgingSession", s009},
		{"S012_CronKanbanAutoWritesRejected", s012},
		{"S013_ACPHoldsSessionExcluded", s013},
		{"S014_TwoPurgesConcurrent", s014},
		{"S015_LargePurgeLockBudget", s015},
		{"S021_GCWriterPurgeTrashRace", s021},
		{"S022_TUIOpenForDaysIdle", s022},
		{"S024_PurgeOwnerDiesTakeover", s024},
		{"S025_ShortProcessPurgePending", s025},
		{"S029_CrossSessionEventRef", s029},
		{"S032_TUIKilledServeScanner", s032},
		{"S034_AutoRefRemovedByPurge", s034},
		{"S038_DiskFullLedgerAndBatch", s038},
		{"S047_DeleteSessionsBlobsRejected", s047},
		{"S048_FreeJSONSessionIdResidue", s048},
		{"S061_PurgeAfterWakeOrCtrlZ", s061},
	}
}

// #2: a stale holder wakes after its session was labelled → guard P.
func s002(t *testing.T) {
	e := newEnv(t)
	a, b := e.open(1, "tui"), e.open(2, "serve")
	session(t, a, "S", "")
	e.clk.Advance(11 * time.Second)
	takeOverB(t, e, b, "S")
	must(t, b.ReleaseLease(context.Background(), "S"))
	purgePhase0(t, b, "S")
	n0 := count(t, b, "SELECT count(*) FROM events")
	err := a.Heartbeat(context.Background(), "S")
	if !errors.Is(err, ErrLeaseFenced) && !errors.Is(err, ErrSessionPurging) {
		t.Fatalf("heartbeat: %v", err)
	}
	a.setToken("S", 999_999) // even a forged token cannot pass guard P
	_, err = a.Append(context.Background(), "S", nil, Event{Kind: "assistant_message"})
	wantErr(t, err, ErrSessionPurging)
	if count(t, b, "SELECT count(*) FROM events") != n0 {
		t.Fatal("event written into labelled session")
	}
}

// #6: journal recovery after an interrupted purge skips labelled sessions.
func s006(t *testing.T) {
	e := newEnv(t)
	a := e.open(1, "tui")
	session(t, a, "S", "")
	must(t, a.Write(context.Background(), func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	e.procs.kill(1)
	b := e.open(2, "doctor")
	// b's startup already recovered T1; build a second journal under a dead
	// owner after labelling to exercise the skip path.
	c := e.open(3, "tui")
	session(t, c, "R", "")
	must(t, c.Write(context.Background(), func(tx *Tx) error { return tx.StartTurn("R", "T2") }))
	must(t, c.ReleaseLease(context.Background(), "R"))
	purgePhase0(t, b, "R")
	rec, err := b.RecoverJournals(context.Background(), true)
	must(t, err)
	for _, r := range rec {
		if r.Session == "R" {
			t.Fatal("recovered a labelled session")
		}
	}
	_, err = c.AcquireLease(context.Background(), "R")
	wantErr(t, err, ErrSessionPurging)
}

// #7: crash at every Append/publication step; file without row removed by
// gc; temp > 24 h removed; committed blobs always have files (I9).
func s007(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	p1, err := s.PutBlob([]byte("temp only"), "text/plain") // crash before publish
	must(t, err)
	p2, err := s.PutBlob([]byte("renamed, rolled back"), "text/plain")
	must(t, err)
	errCrash := errors.New("crash before COMMIT")
	err = s.Write(context.Background(), func(tx *Tx) error { must(t, tx.Publish(p2)); return errCrash })
	wantErr(t, err, errCrash)
	p3, err := s.PutBlob([]byte("committed"), "text/plain")
	must(t, err)
	_, err = s.Append(context.Background(), "S", []*PendingBlob{p3}, Event{Kind: "tool_result", Blob: p3.Sha})
	must(t, err)
	e.clk.Advance(25 * time.Hour)
	must(t, s.Heartbeat(context.Background(), "S"))
	st, err := s.GC(context.Background(), false)
	must(t, err)
	if st.TmpRemoved != 1 || st.OrphanFiles != 1 {
		t.Fatalf("gc stats %+v", st)
	}
	for _, p := range []*PendingBlob{p1, p2} {
		if _, ierr := os.Stat(s.testBlobPath(p.Sha)); !errors.Is(ierr, os.ErrNotExist) {
			t.Fatalf("crash leftover %s remains", p.Sha[:8])
		}
	}
	b, err := s.ReadBlob(context.Background(), p3.Sha)
	must(t, err)
	if string(b) != "committed" {
		t.Fatal("committed blob lost")
	}
}

// #8: fork from a labelled session is rejected; a fork created during phase
// 0 (after preview) excludes its ancestors, the fork and its prefix stay.
func s008(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "A", "")
	session(t, s, "B", "A")
	appendMsg(t, s, "A", "prefix")
	for _, id := range []string{"A", "B"} {
		must(t, s.ReleaseLease(context.Background(), id))
	}
	rep, err := s.purgeSet(context.Background(), PurgeRequest{IDs: []string{"A"}, WithForks: true}) // preview
	must(t, err)
	session(t, s, "F", "A") // new fork after preview
	must(t, s.ReleaseLease(context.Background(), "F"))
	rep.TxID = newID()
	ex, err := s.runPurge(context.Background(), rep.TxID, rep.P, true)
	must(t, err)
	if len(ex) != 1 || ex[0].Reason != "has_forks" || ex[0].IDs[0] != "A" {
		t.Fatalf("excluded %+v", ex)
	}
	if count(t, s, "SELECT count(*) FROM sessions WHERE id IN ('A','F')") != 2 || count(t, s, "SELECT count(*) FROM sessions WHERE id = 'B'") != 0 {
		t.Fatal("wrong survivors")
	}
	if count(t, s, "SELECT count(*) FROM events WHERE session_id = 'A'") != 1 {
		t.Fatal("ancestor prefix lost")
	}
	session(t, s, "X", "")
	must(t, s.ReleaseLease(context.Background(), "X"))
	purgePhase0(t, s, "X")
	_, err = s.CreateSession(context.Background(), Session{ID: "X2", ParentID: "X", Origin: "delegate"})
	wantErr(t, err, ErrSessionPurging)
}

// #9: undo of an action in a session being purged: the lease is refused
// before any file is touched (remaining window reported by the caller).
func s009(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	ev := appendMsg(t, s, "S", "edit")
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO actions(session_id, event_id, kind, reversibility, subject, created_at) VALUES ('S', ?, 'file_write', 'reversible', '/w/a', 0)`, ev)
		return err
	}))
	must(t, s.ReleaseLease(context.Background(), "S"))
	purgePhase0(t, s, "S")
	cli := e.open(2, "cli")
	_, err := cli.AcquireLease(context.Background(), "S") // `damnbot undo` short lease
	wantErr(t, err, ErrSessionPurging)
}

// #12: cron / kanban / auto / curator writes referencing a labelled session.
func s012(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	session(t, s, "S", "")
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		return tx.execAll(nil,
			`INSERT INTO cron_jobs(id, name, schedule, prompt, prompt_sha, cwd, created_at, updated_at) VALUES ('j','j','* * * * *','p','x','/',0,0)`,
			`INSERT INTO kanban_boards(id, name, created_at) VALUES ('b','b',0)`)
	}))
	must(t, s.ReleaseLease(context.Background(), "S"))
	purgePhase0(t, s, "S")
	sys, err := s.Append(context.Background(), SystemSession, nil, Event{Kind: "system_notice"})
	must(t, err)
	for _, q := range []string{
		"INSERT INTO cron_runs(job_id, session_id, scheduled_at) VALUES ('j','S',1)",
		"INSERT INTO kanban_tasks(id, board_id, title, status, session_id, created_at, updated_at) VALUES ('k','b','t','ready','S',0,0)",
		"INSERT INTO auto_session_refs(event_id, session_id) VALUES (" + itoa(sys[0]) + ", 'S')",
		"INSERT INTO memory(kind, scope, content, origin, source_session, created_at) VALUES ('memory','user','x','user','S',0)",
		"INSERT INTO session_labels(session_id, key, value, created_at) VALUES ('S','title','t',0)",
	} {
		err := s.Write(context.Background(), func(tx *Tx) error { _, err := tx.Exec(q); return err })
		wantErr(t, err, ErrSessionPurging)
	}
}

// #13: a live ACP lease → excluded (session_busy), session intact.
func s013(t *testing.T) {
	e := newEnv(t)
	acp, cli := e.open(1, "acp"), e.open(2, "cli")
	session(t, acp, "S", "")
	rep, err := cli.Purge(context.Background(), PurgeRequest{IDs: []string{"S"}})
	wantErr(t, err, ErrSessionBusy)
	if len(rep.Excluded) != 1 || rep.Excluded[0].Reason != "session_busy" || labelledS(t, cli) {
		t.Fatalf("report %+v", rep)
	}
	appendMsg(t, acp, "S", "still mine")
}

// #14: two purges: the second sees the purge row in its first tx.
func s014(t *testing.T) {
	e := newEnv(t)
	a, b := e.open(1, "cli"), e.open(2, "cli")
	for _, id := range []string{"X", "Y"} {
		session(t, a, id, "")
		must(t, a.ReleaseLease(context.Background(), id))
	}
	purgePhase0(t, a, "X") // A stops after phase 0 (still pending)
	_, err := b.Purge(context.Background(), PurgeRequest{IDs: []string{"Y"}})
	wantErr(t, err, ErrPurgeInProgress)
	var wg sync.WaitGroup // concurrent first transactions: never both succeed
	errs := make([]error, 2)
	for i, s := range []*Store{a, b} {
		wg.Go(func() { _, errs[i] = s.Purge(context.Background(), PurgeRequest{IDs: []string{"Y"}}) })
	}
	wg.Wait()
	for _, err := range errs {
		if err == nil {
			t.Fatal("a second purge ran while one is pending")
		}
	}
}

// streamWriter appends to sid every 10 ms (TUI/gateway streaming pace)
// until the returned stop function is called; stop returns its first error.
func streamWriter(s *Store, sid string) func() error {
	stop := make(chan struct{})
	var err error
	var wg sync.WaitGroup
	wg.Go(func() {
		tk := time.NewTicker(10 * time.Millisecond)
		defer tk.Stop()
		for err == nil {
			select {
			case <-stop:
				return
			case <-tk.C:
			}
			_, err = s.Append(context.Background(), sid, nil, Event{Kind: "user_message", Content: "x"})
		}
	})
	return func() error {
		close(stop)
		wg.Wait()
		return err
	}
}

// #15: large purge while TUI and gateway write: every purge tx ≤ 200 ms,
// lock duty ≤ 20 %, 0 schema changes, 0 db_busy for the other writers.
// The two wall-clock budgets (I11) are asserted only under `make test-timing`
// (ADR-048); every correctness invariant is checked in every build.
func s015(t *testing.T) {
	e := newEnv(t)
	enforce := timingEnforced(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var held, maxHeld time.Duration
	minGap, overBudget := time.Hour, 0
	var lastEnd time.Time
	obs := func(mode string, d time.Duration) {
		if mode != modePurge {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if !lastEnd.IsZero() {
			minGap = min(minGap, now.Add(-d).Sub(lastEnd))
		}
		lastEnd, held, maxHeld = now, held+d, max(maxHeld, d)
		if d > 200*time.Millisecond {
			if overBudget++; overBudget == 3 && enforce {
				cancel() // the budget is already violated: stop instead of running for minutes
			}
		}
	}
	p := e.open(1, "cli", func(o *Options) { o.Clock = SystemClock{}; o.OnTx = obs })
	tui := e.open(2, "tui", func(o *Options) { o.Clock = SystemClock{} })
	session(t, p, "BIG", "")
	// Under -race the instrumented engine makes the purge unit stay at a few
	// events per transaction (yield ≥ 4× hold), so 5 000 events would take
	// > 10 min. The race build purges a smaller corpus — still dozens of
	// transactions against the live writer — and checks the same correctness
	// invariants; the full corpus and its budgets run in `make test-timing`.
	batches, perBatch := 10, 500 // 5 000 events
	if raceEnabled {
		batches, perBatch = 1, 100
	}
	evs := make([]Event, perBatch)
	for i := range evs {
		evs[i] = Event{Kind: "tool_result", Content: "output line"}
	}
	for range batches {
		_, err := p.Append(context.Background(), "BIG", nil, evs...)
		must(t, err)
	}
	must(t, p.ReleaseLease(context.Background(), "BIG"))
	session(t, tui, "LIVE", "")
	var sv0, sv1 int64
	must(t, p.get(context.Background(), "PRAGMA schema_version", nil, &sv0))
	stopWriter := streamWriter(tui, "LIVE")
	start := time.Now()
	_, err := p.Purge(ctx, PurgeRequest{IDs: []string{"BIG"}})
	total := time.Since(start)
	busy := stopWriter()
	t.Logf("max tx %v, min gap %v, txns over 200 ms: %d", maxHeld, minGap, overBudget)
	must(t, busy) // writers are never starved by the purge (yield ≥ busy-handler backoff)
	checkBudget(t, "I11 purge tx", maxHeld, 200*time.Millisecond)
	checkBudget(t, "I11 lock duty (20 % of purge wall time)", held, total/5)
	must(t, err)
	must(t, p.get(context.Background(), "PRAGMA schema_version", nil, &sv1))
	if sv0 != sv1 {
		t.Fatal("schema changed during purge")
	}
	if n := count(t, p, "SELECT count(*) FROM sessions WHERE id = 'BIG'"); n != 0 {
		t.Fatal("purged session still present")
	}
	checkIntegrity(t, p)
	t.Logf("purge %d events: %v, max tx %v, min gap %v, duty %.1f%%", batches*perBatch, total, maxHeld, minGap, 100*float64(held)/float64(total))
}

// #21: gc vs writer vs purge vs trash recovery → 0 live refs to missing files.
func s021(t *testing.T) {
	e := newEnv(t)
	w, g, p := e.open(1, "tui"), e.open(2, "serve"), e.open(3, "cli")
	session(t, w, "W", "")
	session(t, p, "P", "")
	shared := []byte("shared payload")
	pb, err := p.PutBlob(shared, "text/plain")
	must(t, err)
	_, err = p.Append(context.Background(), "P", []*PendingBlob{pb}, Event{Kind: "tool_result", Blob: pb.Sha})
	must(t, err)
	must(t, p.ReleaseLease(context.Background(), "P"))
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Go(func() {
		for range 60 {
			b, err := w.PutBlob(shared, "text/plain")
			if err == nil {
				_, err = w.Append(context.Background(), "W", []*PendingBlob{b}, Event{Kind: "tool_result", Blob: b.Sha})
			}
			if err != nil {
				errs <- err
				return
			}
		}
	})
	wg.Go(func() {
		for range 10 {
			if _, err := g.GC(context.Background(), false); err != nil {
				errs <- err
				return
			}
		}
	})
	wg.Go(func() {
		if _, err := p.Purge(context.Background(), PurgeRequest{IDs: []string{"P"}}); err != nil {
			errs <- err
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	assertLiveBlobsHaveFiles(t, g)
}

// assertLiveBlobsHaveFiles checks I9 over every ref.
func assertLiveBlobsHaveFiles(t *testing.T, s *Store) {
	t.Helper()
	shas, err := s.strings(context.Background(), `SELECT DISTINCT r.sha256 FROM blob_refs r WHERE coalesce((SELECT kind FROM blob_expirations e
		WHERE e.sha256 = r.sha256 ORDER BY e.id DESC LIMIT 1), '') <> 'expired'`)
	must(t, err)
	for _, sha := range shas {
		if _, err := s.ReadBlob(context.Background(), sha); err != nil {
			t.Fatalf("live ref %s: %v", sha[:8], err)
		}
	}
}

// #22: TUI open for days with no running turn does not block idle gc.
func s022(t *testing.T) {
	e := newEnv(t)
	tui, srv := e.open(1, "tui"), e.open(2, "serve")
	session(t, tui, "S", "")
	for range 3 * 24 * 60 { // three days of heartbeats every minute
		e.clk.Advance(time.Minute)
		must(t, tui.Heartbeat(context.Background(), "S"))
	}
	idle, err := srv.Idle(context.Background())
	must(t, err)
	if !idle {
		t.Fatal("open TUI without running turn blocks gc")
	}
	must(t, tui.Write(context.Background(), func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	if idle, err := srv.Idle(context.Background()); err != nil || idle {
		t.Fatal("running turn with alive lease counted idle")
	}
}

// #24: the purge owner dies → lease taken over (case (a)) and completed;
// an owner on another host is taken over after TTL + two observations.
func s024(t *testing.T) {
	e := newEnv(t)
	a := e.open(1, "cli")
	session(t, a, "S", "")
	must(t, a.ReleaseLease(context.Background(), "S"))
	purgePhase0(t, a, "S")
	e.procs.kill(1)
	srv := e.open(2, "serve")
	must(t, srv.ResumePurge(context.Background(), false))
	if count(t, srv, "SELECT count(*) FROM sessions WHERE id = 'S'") != 0 {
		t.Fatal("purge not completed after takeover")
	}
	o := e.open(3, "cli", func(o *Options) { o.Ident.Host = "elsewhere#1" })
	session(t, o, "R", "")
	must(t, o.ReleaseLease(context.Background(), "R"))
	purgePhase0(t, o, "R")
	err := srv.ResumePurge(context.Background(), true)
	wantErr(t, err, ErrPurgeInProgress) // still within TTL 30 s
	e.clk.Advance(31 * time.Second)
	wantErr(t, srv.ResumePurge(context.Background(), true), ErrPurgeInProgress) // first observation
	e.clk.Advance(observeGap * time.Millisecond)
	must(t, srv.ResumePurge(context.Background(), true))
	checkIntegrity(t, srv)
}

// #25: a short-lived process only reports purge_pending.
func s025(t *testing.T) {
	e := newEnv(t)
	a := e.open(1, "cli")
	session(t, a, "S", "")
	must(t, a.ReleaseLease(context.Background(), "S"))
	purgePhase0(t, a, "S")
	e.procs.kill(1)
	cli := e.open(2, "cli")
	wantErr(t, cli.ResumePurge(context.Background(), false), ErrPurgePending)
	if !labelledS(t, cli) || count(t, cli, "SELECT count(*) FROM sessions WHERE id = 'S'") != 1 {
		t.Fatal("short process touched the purge")
	}
}

// #29: a row of session B carrying an event_id of session A is rejected.
func s029(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "A", "")
	session(t, s, "B", "")
	evA := appendMsg(t, s, "A", "a")
	q := "INSERT INTO usage_ledger(session_id, event_id, purpose, provider, model, input_tokens, output_tokens, cost_source, pricing_version, attempts, latency_ms, created_at) VALUES ('B', ?, 'turn', 'p', 'm', 1, 1, 'unknown', 'v', 1, 1, 0)"
	err := s.Write(context.Background(), func(tx *Tx) error { _, err := tx.Exec(q, evA); return err })
	wantErr(t, err, ErrInternal)
	must(t, s.ReleaseLease(context.Background(), "A"))
	purgePhase0(t, s, "A")
	err = s.Write(context.Background(), func(tx *Tx) error {
		_, ierr := tx.Exec("INSERT INTO approvals(session_id, event_id, tool_name, subject, decision, source, created_at) VALUES ('B', ?, 't', 's', 'deny', 'tui', 0)", evA)
		return ierr
	})
	if err == nil {
		t.Fatal("row referencing a labelled session's event accepted")
	}
}

// #32: TUI kill -9 while serve runs, no new process → serve's 60 s scanner.
func s032(t *testing.T) {
	e := newEnv(t)
	srv, tui := e.open(1, "serve"), e.open(2, "tui")
	session(t, tui, "S", "")
	must(t, tui.Write(context.Background(), func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	e.procs.kill(2)
	if idle, err := srv.Idle(context.Background()); err != nil || !idle {
		t.Fatal("orphan journal of a dead owner blocks idle (I12)")
	}
	rec, err := srv.RecoverJournals(context.Background(), true)
	must(t, err)
	if len(rec) != 1 || rec[0].Decision != "deferred" {
		t.Fatalf("recovered %+v", rec)
	}
}

// #34: auto feature refs to a purged session are removed (I13).
func s034(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	session(t, s, "S", "")
	sys, err := s.Append(context.Background(), SystemSession, nil, Event{Kind: "system_notice", Content: "insight"})
	must(t, err)
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		_, ierr := tx.Exec("INSERT INTO auto_session_refs(event_id, session_id) VALUES (?, 'S')", sys[0])
		return ierr
	}))
	must(t, s.ReleaseLease(context.Background(), "S"))
	_, err = s.Purge(context.Background(), PurgeRequest{IDs: []string{"S"}})
	must(t, err)
	if n := count(t, s, "SELECT count(*) FROM auto_session_refs"); n != 0 {
		t.Fatalf("auto_session_refs left: %d", n)
	}
	assertNoSessionRefs(t, s, "S")
}

// assertNoSessionRefs is I13 over every FK column to sessions.
func assertNoSessionRefs(t *testing.T, s *Store, sid string) {
	t.Helper()
	for _, q := range []string{
		"SELECT count(*) FROM events WHERE session_id = ?1", "SELECT count(*) FROM session_labels WHERE session_id = ?1",
		"SELECT count(*) FROM session_leases WHERE session_id = ?1", "SELECT count(*) FROM blob_refs WHERE session_id = ?1",
		"SELECT count(*) FROM usage_ledger WHERE session_id = ?1", "SELECT count(*) FROM turn_journal WHERE session_id = ?1",
		"SELECT count(*) FROM gateway_conversations WHERE session_id = ?1", "SELECT count(*) FROM memory WHERE source_session = ?1",
		"SELECT count(*) FROM cron_runs WHERE session_id = ?1", "SELECT count(*) FROM kanban_tasks WHERE session_id = ?1",
		"SELECT count(*) FROM auto_session_refs WHERE session_id = ?1", "SELECT count(*) FROM sessions WHERE id = ?1 OR parent_id = ?1",
	} {
		if n := count(t, s, q, sid); n != 0 {
			t.Fatalf("%s → %d", q, n)
		}
	}
	checkIntegrity(t, s)
}

// #38: ledger append fails → purge never starts; SQLITE_FULL mid-batch →
// rollback, purge resumes once space is back.
func s038(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	must(t, s.ReleaseLease(context.Background(), "S"))
	must(t, os.Mkdir(filepath.Join(e.dir, "purge.log"), 0o700)) // append impossible
	_, err := s.Purge(context.Background(), PurgeRequest{IDs: []string{"S"}})
	wantErr(t, err, ErrDiskFull)
	if count(t, s, "SELECT count(*) FROM module_state WHERE module_id = 'purge'") != 0 || labelledS(t, s) {
		t.Fatal("purge started without ledger intent")
	}
	must(t, os.Remove(filepath.Join(e.dir, "purge.log")))
	var pages int64
	must(t, s.get(context.Background(), "PRAGMA page_count", nil, &pages))
	_, err = s.w.ExecContext(context.Background(), "PRAGMA max_page_count = "+itoa(pages))
	must(t, err)
	for range 20_000 { // fill every page under the cap
		if _, ierr := s.Append(context.Background(), SystemSession, nil, Event{Kind: "system_notice", Content: "f"}); ierr != nil {
			wantErr(t, ierr, ErrDiskFull)
			break
		}
	}
	_, err = s.Purge(context.Background(), PurgeRequest{IDs: []string{"S"}})
	wantErr(t, err, ErrDiskFull)
	_, err = s.w.ExecContext(context.Background(), "PRAGMA max_page_count = 1073741823")
	must(t, err)
	if _, perr := s.PendingPurge(context.Background()); perr == nil {
		must(t, s.ResumePurge(context.Background(), false))
	} else {
		_, err = s.Purge(context.Background(), PurgeRequest{IDs: []string{"S"}})
		must(t, err)
	}
	assertNoSessionRefs(t, s, "S")
}

// #47: DELETE sessions/blobs from a non-purge connection.
func s047(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	pb, err := s.PutBlob([]byte("b"), "text/plain")
	must(t, err)
	_, err = s.Append(context.Background(), "S", []*PendingBlob{pb}, Event{Kind: "tool_result", Blob: pb.Sha})
	must(t, err)
	for _, q := range []string{"DELETE FROM sessions WHERE id = 'S'", "DELETE FROM blobs", "UPDATE sessions SET cwd = 'x'", "UPDATE blobs SET mime = 'x'", "DELETE FROM events"} {
		if err := s.Write(context.Background(), func(tx *Tx) error { _, err := tx.Exec(q); return err }); err == nil {
			t.Fatalf("%s accepted", q)
		}
	}
}

// #48: session ids inside free JSON payloads survive purge — documented
// residue; structured references are gone (I13).
func s048(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	session(t, s, "S", "")
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		return tx.execAll(nil, "INSERT INTO kanban_boards(id, name, created_at) VALUES ('b','b',0)",
			"INSERT INTO kanban_tasks(id, board_id, title, status, created_at, updated_at) VALUES ('k','b','t','done',0,0)",
			`INSERT INTO kanban_task_events(task_id, actor, kind, payload, created_at) VALUES ('k','agent','comment','{"session":"S"}',0)`)
	}))
	must(t, s.ReleaseLease(context.Background(), "S"))
	_, err := s.Purge(context.Background(), PurgeRequest{IDs: []string{"S"}})
	must(t, err)
	assertNoSessionRefs(t, s, "S")
	if count(t, s, `SELECT count(*) FROM kanban_task_events WHERE payload LIKE '%"S"%'`) != 1 {
		t.Fatal("residue expectation changed: update TRUST-MODEL")
	}
}

// #61: purge right after wake-up or while the TUI is Ctrl-Z'd: expired lease
// of a present owner is not takeover_ok → excluded.
func s061(t *testing.T) {
	e := newEnv(t)
	tui, cli := e.open(1, "tui"), e.open(2, "cli")
	session(t, tui, "S", "")
	must(t, tui.Suspend())
	e.clk.Suspend(20 * time.Minute)
	_, err := cli.Purge(context.Background(), PurgeRequest{IDs: []string{"S"}})
	wantErr(t, err, ErrSessionBusy)
	if labelledS(t, cli) {
		t.Fatal("labelled a session whose owner is present")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
