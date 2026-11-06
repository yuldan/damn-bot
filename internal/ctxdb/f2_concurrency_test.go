package ctxdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestConcurrencyF2 is the F2 half of SCORECARD §1.3 "Konkurensi" (ADR-065):
// 4 processes × 50 turns on their own sessions plus 2 processes contending on
// one shared session, with zero ErrDBBusy and an intact database. It is also
// the Damn-Bot driver of the F2 baseline harness (bench/f2/run.sh), which
// parses the single F2-CONCURRENCY line.
func TestConcurrencyF2(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "serve"})
	must(t, err)
	if _, cerr := s.CreateSession(context.Background(), Session{ID: "SHARED"}); cerr != nil {
		must(t, cerr)
	}
	must(t, s.ReleaseLease(context.Background(), "SHARED"))
	must(t, s.Close())

	var kids []*child
	for range 4 {
		kids = append(kids, startChild(t, "f2-own", dir))
	}
	for range 2 {
		kids = append(kids, startChild(t, "f2-shared", dir))
	}
	own, shared, busy, errs := 0, 0, 0, 0
	for i, c := range kids {
		res := c.result(t)
		busy += res.Busy
		errs += len(res.Errors)
		if i < 4 {
			own += res.Own
			if res.Own != 100 || len(res.Errors) != 0 {
				t.Fatalf("own worker %d: appends=%d errors=%v", i, res.Own, res.Errors)
			}
		} else {
			shared += res.Shared
			if res.Shared != 100 || len(res.Errors) != 0 {
				t.Fatalf("shared worker %d: appends=%d errors=%v", i, res.Shared, res.Errors)
			}
		}
	}
	store, err := Open(context.Background(), &Options{Dir: dir, Kind: "serve"})
	must(t, err)
	defer func() { must(t, store.Close()) }()
	checkIntegrity(t, store)
	if n := count(t, store, "SELECT count(*) FROM events WHERE session_id = 'SHARED'"); n != int64(shared) {
		t.Fatalf("shared events %d, reported %d", n, shared)
	}
	out, err := json.Marshal(struct {
		Agent         string `json:"agent"`
		Workers       int    `json:"workers"`
		SharedWorkers int    `json:"shared_workers"`
		Turns         int    `json:"turns"`
		OwnAppends    int    `json:"own_appends"`
		SharedAppends int    `json:"shared_appends"`
		Busy          int    `json:"busy"`
		Errors        int    `json:"errors"`
		Integrity     string `json:"integrity"`
	}{
		Agent: "damnbot", Workers: 4, SharedWorkers: 2, Turns: 50,
		OwnAppends: own, SharedAppends: shared, Busy: busy, Errors: errs, Integrity: "ok",
	})
	must(t, err)
	if _, werr := fmt.Fprintf(os.Stdout, "F2-CONCURRENCY: %s\n", out); werr != nil {
		t.Fatal(werr)
	}
}

const (
	// f2LeaseHangGuard bounds one wait for the SHARED lease. It is a deadlock
	// detector, not a performance budget: a waiting worker is guaranteed only
	// eventual progress, never a turn inside any wall-clock window, because the
	// peer may legitimately hold the lease for its remaining turns (the whole
	// test takes ~2 min under -race on CI, and a single wait was measured at
	// >12 s). Only a holder that never releases reaches the guard.
	f2LeaseHangGuard = 4 * time.Minute
	// f2UnreleasedGuard is the same guard for the negative control, where the
	// holder never releases: any bound detects it, the value only sets how long
	// the control runs.
	f2UnreleasedGuard = 250 * time.Millisecond
)

// waitSharedLease waits until the SHARED lease belongs to this store.
// The protocol is a release handshake, not a poll loop with a deadline: the
// holder signals its release by committing the lease-row delete in
// ReleaseLease, and the waiter only attempts AcquireLease after observing that
// row gone on a read connection. A busy AcquireLease means another contender
// won the free window, so the waiter goes back to watching for the next
// release instead of retrying the writer (a tight loop of busy writers queues
// ahead of the holder's own Append — the Windows -race starvation). The guard
// fires only when no release is ever observed.
func waitSharedLease(ctx context.Context, s *Store, guard time.Duration) error {
	start := time.Now()
	for {
		// Wait on a read connection while a live foreign owner holds the lease.
		// Only the moment the lease looks free costs a writer.
		pid, held := sharedLeaseHeld(ctx, s, "SHARED")
		if !held {
			_, err := s.AcquireLease(ctx, "SHARED")
			switch {
			case err == nil:
				return nil
			case !errors.Is(err, ErrSessionBusy):
				return err
			}
			// Lost the free window: the winner owns the lease now, so wait for
			// its release instead of retrying the writer. pid stays 0 when it
			// was released again before this probe, and the loop retries.
			pid, _ = sharedLeaseHeld(ctx, s, "SHARED")
		}
		if time.Since(start) >= guard {
			return leaseWaitExhausted(pid, guard)
		}
		runtime.Gosched()
	}
}

// appendSharedTurn acquires the SHARED lease (the two shared workers contend
// for it), appends one turn and releases; losing the race is a reason to wait
// for the next release, not an error.
func appendSharedTurn(s *Store, i int, res *multiResult) int {
	ctx := context.Background()
	start := time.Now()
	if err := waitSharedLease(ctx, s, f2LeaseHangGuard); err != nil {
		res.note("acquire", start, err)
		return 0
	}
	n := 0
	_, err := s.Append(ctx, "SHARED", nil,
		Event{Kind: "user_message", Content: fmt.Sprintf("shared u%d", i)},
		Event{Kind: "assistant_message", Content: fmt.Sprintf("shared a%d", i)},
	)
	if err == nil {
		n = 2
	} else {
		res.note("append", start, err)
	}
	res.note("release", start, s.ReleaseLease(ctx, "SHARED"))
	return n
}

// sharedLeaseHeld reports a live foreign owner of sid from a read connection,
// mirroring the busy branch of Tx.AcquireLease so a waiting contender never
// takes the writer lock.
func sharedLeaseHeld(ctx context.Context, s *Store, sid string) (int, bool) {
	var x liveRec
	var old int64
	if err := s.get(ctx, s.q["lease.get"], []any{sid},
		&old, &x.PID, &x.Start, &x.Host, &x.BootID, &x.HeartbeatAt, &x.ExpiresAt); err != nil {
		return 0, false
	}
	if mine, _ := s.token(sid); mine == old || x.Ident == s.id || s.takeoverOK("lease:"+sid, x, false) {
		return 0, false
	}
	return x.PID, true
}

// leaseWaitExhausted is the failure a lease wait reports when its hang guard
// fires: no release was observed, so the holder is stalled rather than slow.
func leaseWaitExhausted(pid int, guard time.Duration) error {
	return fmt.Errorf("ctxdb: session SHARED held by pid %d with no release within %v: %w", pid, guard, ErrSessionBusy)
}

// f2Worker appends turns (one user + one assistant event per turn) to its own
// session or to the shared one, counting busy and other errors like the
// multi-process chaos worker.
func f2Worker(t *testing.T, s *Store, shared bool, turns int) multiResult {
	t.Helper()
	var res multiResult
	sid := "SHARED"
	if !shared {
		sid = fmt.Sprintf("own-%d", os.Getpid())
		if _, err := s.CreateSession(context.Background(), Session{ID: sid}); err != nil {
			res.note("create", time.Now(), err)
			return res
		}
	}
	for i := range turns {
		if shared {
			res.Shared += appendSharedTurn(s, i, &res)
			continue
		}
		start := time.Now()
		_, err := s.Append(context.Background(), sid, nil,
			Event{Kind: "user_message", Content: fmt.Sprintf("u%d", i)},
			Event{Kind: "assistant_message", Content: fmt.Sprintf("a%d", i)},
		)
		if err != nil {
			res.note("append", start, err)
			continue
		}
		res.Own += 2
	}
	return res
}

// TestSharedLeaseWaitDetectsUnreleasedOwner is the negative control for the
// release handshake: a SHARED lease held by a live foreign process that never
// releases it must end the wait with ErrSessionBusy when the guard fires, not
// be mistaken for a slow peer that will eventually yield a turn — otherwise a
// leaked lease would let the test pass with appends=0. f2UnreleasedGuard only
// shortens the control; the ordering stays the same as in the real wait.
func TestSharedLeaseWaitDetectsUnreleasedOwner(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "serve"})
	must(t, err)
	defer func() { must(t, s.Close()) }()
	if _, cerr := s.CreateSession(context.Background(), Session{ID: "SHARED"}); cerr != nil {
		must(t, cerr)
	}
	must(t, s.ReleaseLease(context.Background(), "SHARED"))
	c := startChild(t, "hold-lease", dir) // READY only after it owns the lease
	start := time.Now()
	werr := waitSharedLease(context.Background(), s, f2UnreleasedGuard)
	if !errors.Is(werr, ErrSessionBusy) {
		t.Fatalf("wait for a lease that is never released = %v after %v, want session_busy",
			werr, time.Since(start))
	}
	t.Logf("unreleased lease of pid %d detected after %v: %v", c.cmd.Process.Pid, time.Since(start), werr)
}
