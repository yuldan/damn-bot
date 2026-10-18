package ctxdb

import (
	"os"
	"testing"
	"time"
)

// TestScenario runs the adversarial scenarios of SPEC §11.11.13 (#1–#78)
// and REVIEW-1.0 §"Verifikasi ADR-046" (#79–#86), one subtest per row.
func TestScenario(t *testing.T) {
	for _, sc := range scenarioTable() {
		t.Run(sc.name, sc.fn)
	}
}

type scenario struct {
	name string
	fn   func(*testing.T)
}

func scenarioTable() []scenario {
	return append(append(append(append(leaseScenarios(), purgeScenarios()...), maintScenarios()...),
		gatewayScenarios()...), append(approvalScenarios(), procScenarios()...)...)
}

// acquireBg acquires a lease as a background scanner (takeover case (b) allowed).
func acquireBg(s *Store, sid string) (tok int64, err error) {
	err = s.Write(bg, func(t *Tx) error { tok, err = t.AcquireLease(sid, true); return err })
	return tok, err
}

// takeOverB performs the two-observation takeover (owner still present).
func takeOverB(t *testing.T, e *env, s *Store, sid string) int64 {
	t.Helper()
	if _, err := acquireBg(s, sid); err == nil {
		t.Fatal("takeover on first observation")
	}
	e.clk.Advance(observeGap * time.Millisecond)
	tok, err := acquireBg(s, sid)
	must(t, err)
	return tok
}

// purgePhase0 runs only phase 0 of a purge (as if the purge process died
// right after labelling), returning the txid.
func purgePhase0(t *testing.T, s *Store, ids ...string) string {
	t.Helper()
	rep, err := s.purgeSet(bg, PurgeRequest{IDs: ids, WithForks: true})
	must(t, err)
	txid := newID()
	must(t, s.ledger(map[string]any{"type": "intent", "txid": txid, "P": rep.P}))
	for i := 0; ; i++ {
		var v purgeValue
		must(t, s.tx(bg, modePurge, func(tx *Tx) (err error) { v, err = tx.purgeBatch(txid, rep.P, i == 0); return err }))
		if v.Phase != 0 {
			return txid
		}
	}
}

func isLabelled(t *testing.T, s *Store, sid string) bool {
	t.Helper()
	return count(t, s, "SELECT count(*) FROM session_labels WHERE session_id = ? AND key = 'purge_state'", sid) > 0
}

func leaseScenarios() []scenario {
	return []scenario{
		{"S001_SigstopTakeoverThenSigcont", s001},
		{"S003_SleepAndWallClockStep", s003},
		{"S004_RebootMidTurn", s004},
		{"S005_TwoRecoverersSameJournal", s005},
		{"S023_PIDReuseAndNamespace", s023},
		{"S027_WallStepThenSecondProcess", s027},
		{"S033_SigstopAfterToolPendingCommit", s033},
		{"S039_LeaseFromPreviousBootNoBootID", s039},
		{"S040_TimeNamespaceMismatchFencingHolds", s040},
		{"S044_StartStampComparedVerbatim", s044},
		{"S046_WakeBeforeOwnerHeartbeat", s046},
		{"S056_SigtstpLongStopFenced", s056},
	}
}

// #1: holder stopped > 10 s, taken over, then SIGCONT → every write fenced.
func s001(t *testing.T) {
	e := newEnv(t)
	a, b := e.open(1, "tui"), e.open(2, "serve")
	session(t, a, "S", "")
	must(t, a.Write(bg, func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	e.clk.Advance(11 * time.Second) // A stopped: no heartbeat
	tok := takeOverB(t, e, b, "S")
	before := count(t, b, "SELECT count(*) FROM events WHERE session_id = 'S'")
	wantErr(t, a.Heartbeat(bg, "S"), ErrLeaseFenced)
	_, err := a.Append(bg, "S", nil, Event{Kind: "assistant_message", Content: "late"})
	wantErr(t, err, ErrLeaseFenced)
	wantErr(t, a.CheckLease(bg, "S"), ErrLeaseFenced) // checkpoint before LLM/tool → turn cancelled
	if n := count(t, b, "SELECT count(*) FROM events WHERE session_id = 'S'"); n != before {
		t.Fatalf("stale writer added %d events", n-before)
	}
	if got := count(t, b, "SELECT fence_token FROM session_leases WHERE session_id = 'S'"); got != tok {
		t.Fatalf("token %d, want %d", got, tok)
	}
}

// #3 / #46: sleep 10 min and wall step ±1 h → no takeover of a live owner.
func s003(t *testing.T) {
	e := newEnv(t)
	a, b := e.open(1, "tui"), e.open(2, "serve")
	session(t, a, "S", "")
	for _, d := range []time.Duration{time.Hour, -time.Hour} {
		e.clk.StepWall(d)
		_, err := acquireBg(b, "S")
		wantErr(t, err, ErrSessionBusy)
	}
	e.clk.Suspend(10 * time.Minute) // all leases look expired at wake-up
	_, err := acquireBg(b, "S")     // first observation only
	wantErr(t, err, ErrSessionBusy)
	must(t, a.Heartbeat(bg, "S")) // owner heartbeats within 2 s of waking
	e.clk.Advance(3 * time.Second)
	_, err = acquireBg(b, "S")
	wantErr(t, err, ErrSessionBusy)
	must(t, a.CheckLease(bg, "S"))
}

// #4: reboot mid-turn → boot_id differs and pid gone → orphan cleaned.
func s004(t *testing.T) {
	e := newEnv(t)
	a := e.open(1, "tui")
	session(t, a, "S", "")
	must(t, a.Write(bg, func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	e.boot = "boot-2" // new kernel boot; old pid does not exist any more
	e.procs.kill(1)
	b := e.open(2, "tui") // startup recovery (case (a), synchronous)
	if n := count(t, b, "SELECT count(*) FROM events WHERE session_id = 'S' AND kind = 'crash_recovered'"); n != 1 {
		t.Fatalf("crash_recovered = %d", n)
	}
	if st := count(t, b, "SELECT count(*) FROM turn_journal WHERE turn_id = 'T1' AND state = 'resumed'"); st != 1 {
		t.Fatal("journal not resumed by origin frontend")
	}
}

// #5: two processes recover the same journal → lease serialises, one wins.
func s005(t *testing.T) {
	e := newEnv(t)
	a := e.open(1, "tui")
	session(t, a, "S", "")
	must(t, a.Write(bg, func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	e.procs.kill(1)
	b, c := e.open(2, "cli"), e.open(3, "cli") // b's startup already recovers
	done := make(chan error, 2)
	for _, s := range []*Store{b, c} {
		go func() { _, err := s.RecoverJournals(bg, false); done <- err }()
	}
	must(t, <-done)
	must(t, <-done)
	if n := count(t, b, "SELECT count(*) FROM events WHERE kind = 'crash_recovered'"); n != 1 {
		t.Fatalf("crash_recovered = %d, want 1", n)
	}
}

// #23: PID reuse (same pid, other start stamp) = gone; other pid namespace
// never judged by pid, only by expiry + two observations.
func s023(t *testing.T) {
	self := SelfIdent()
	sp := SystemProcs{}
	if sp.Gone(self) {
		t.Fatal("self judged gone")
	}
	reused := self
	reused.Start = "1"
	if !sp.Gone(reused) {
		t.Fatal("pid reuse not detected")
	}
	unknown := self
	unknown.Start = ""
	if sp.Gone(unknown) {
		t.Fatal("unknown start stamp must count as present")
	}
	e := newEnv(t)
	a := e.open(1, "tui", func(o *Options) { o.Ident.Host = "otherhost#4026532000" })
	b := e.open(2, "serve")
	session(t, a, "S", "")
	e.procs.kill(1) // pid 1 dead in *our* namespace must not matter
	e.clk.Advance(11 * time.Second)
	_, err := b.AcquireLease(bg, "S") // synchronous path: case (a) only
	wantErr(t, err, ErrSessionBusy)
	takeOverB(t, e, b, "S")
}

// #27: wall step on any OS, then a second process starts → lease intact.
func s027(t *testing.T) {
	e := newEnv(t)
	a := e.open(1, "tui")
	session(t, a, "S", "")
	must(t, a.Write(bg, func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	e.clk.StepWall(-time.Hour)
	b := e.open(2, "acp") // startup recovery must not touch the live turn
	if n := count(t, b, "SELECT count(*) FROM events WHERE kind = 'crash_recovered'"); n != 0 {
		t.Fatal("live turn recovered after wall step")
	}
	must(t, a.CheckLease(bg, "S"))
}

// #33: SIGSTOP right after COMMIT tool_pending → takeover reports
// outcome_unknown and asks; the stale owner is fenced at its next check.
func s033(t *testing.T) {
	e := newEnv(t)
	a, b := e.open(1, "tui"), e.open(2, "serve")
	session(t, a, "S", "")
	must(t, a.Write(bg, func(tx *Tx) error {
		if err := tx.StartTurn("S", "T1"); err != nil {
			return err
		}
		return tx.ToolPending("S", "T1", ToolCall{CallID: "c1", Name: "shell", Class: "non_idempotent", ArgsSha: "a"})
	}))
	e.clk.Advance(11 * time.Second)
	e.clk.Advance(observeGap * time.Millisecond)
	_, _ = b.RecoverJournals(bg, true) // first observation
	e.clk.Advance(observeGap * time.Millisecond)
	rec, err := b.RecoverJournals(bg, true)
	must(t, err)
	if len(rec) != 1 || rec[0].Status != "outcome_unknown" || rec[0].Decision != "ask" {
		t.Fatalf("recovered %+v", rec)
	}
	wantErr(t, a.CheckLease(bg, "S"), ErrLeaseFenced)
}

// #39: OS without boot_id: heartbeat stamp from the future = other boot.
func s039(t *testing.T) {
	e := newEnv(t)
	e.boot = ""
	s := e.open(1, "tui")
	now := e.clk.BootMs()
	future := liveRec{Ident: Ident{Host: "h#1", PID: 9}, HeartbeatAt: now + 5_000, ExpiresAt: now + 15_000}
	if s.alive(future) {
		t.Fatal("future heartbeat judged alive")
	}
	past := liveRec{Ident: Ident{Host: "h#1", PID: 9}, HeartbeatAt: now - 1_000, ExpiresAt: now + 9_000}
	if !s.alive(past) {
		t.Fatal("unexpired lease without boot_id judged dead")
	}
	e.clk.Advance(LeaseTTL * time.Millisecond)
	if s.alive(past) {
		t.Fatal("lease alive after TTL")
	}
}

// #40: time namespace offset (unsupported): liveness may err, fencing still
// prevents a second writer; doctor detects heartbeat_at > now + 2 s.
func s040(t *testing.T) {
	e := newEnv(t)
	a, b := e.open(1, "tui"), e.open(2, "serve")
	session(t, a, "S", "")
	must(t, a.Write(bg, func(tx *Tx) error {
		_, err := tx.Exec("UPDATE session_leases SET heartbeat_at = heartbeat_at + 60000 WHERE session_id = 'S'")
		return err
	}))
	if n := count(t, b, "SELECT count(*) FROM session_leases WHERE owner_boot_id = ? AND heartbeat_at > ?", e.boot, e.clk.BootMs()+2000); n != 1 {
		t.Fatal("clock_namespace_mismatch not detectable")
	}
	e.procs.kill(1)
	_, err := b.AcquireLease(bg, "S")
	must(t, err)
	_, err = a.Append(bg, "S", nil, Event{Kind: "assistant_message"})
	wantErr(t, err, ErrLeaseFenced)
}

// #44: start stamps are opaque strings compared exactly; wall steps do not
// change them, so criterion 3 is unaffected.
func s044(t *testing.T) {
	st, err := procStart(os.Getpid())
	must(t, err)
	if st == "" {
		t.Fatal("no start stamp on Linux")
	}
	e := newEnv(t)
	e.clk.StepWall(time.Hour)
	st2, _ := procStart(os.Getpid())
	if st != st2 || (SystemProcs{}).Gone(Ident{PID: os.Getpid(), Start: st}) {
		t.Fatal("start stamp changed or self gone")
	}
	_ = e
}

// #46: covered by the sleep half of #3 with a scanner observing first.
func s046(t *testing.T) {
	e := newEnv(t)
	a, b := e.open(1, "tui"), e.open(2, "serve")
	session(t, a, "S", "")
	e.clk.Suspend(30 * time.Minute)
	for range 3 { // scanner keeps observing before the owner heartbeats
		_, err := acquireBg(b, "S")
		wantErr(t, err, ErrSessionBusy)
	}
	must(t, a.Heartbeat(bg, "S"))
	e.clk.Advance(5 * time.Second)
	_, err := acquireBg(b, "S")
	wantErr(t, err, ErrSessionBusy)
}

// #56: SIGTSTP (Suspend) holding a lease for long: takeover needs two
// observations; on SIGCONT CheckLease reports lease_fenced.
func s056(t *testing.T) {
	e := newEnv(t)
	a, b := e.open(1, "tui"), e.open(2, "serve")
	session(t, a, "S", "")
	a.Suspend()
	e.clk.Advance(15 * time.Second)
	takeOverB(t, e, b, "S")
	wantErr(t, a.CheckLease(bg, "S"), ErrLeaseFenced) // SIGCONT: reattach, then the check point fails
	_, err := a.Append(bg, "S", nil, Event{Kind: "assistant_message"})
	wantErr(t, err, ErrLeaseFenced)
}
