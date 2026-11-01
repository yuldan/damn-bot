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

// appendSharedTurn acquires the SHARED lease (the two shared workers contend
// for it), appends one turn and releases; lease-busy is retried, not an error.
func appendSharedTurn(s *Store, i int, res *multiResult) int {
	ctx := context.Background()
	start := time.Now()
	deadline := start.Add(10 * time.Second)
	for {
		// Wait on a read connection while a live foreign owner holds the lease.
		// A busy AcquireLease opens a write transaction, and a tight loop of
		// those queues ahead of the owner's own Append: that is what starved
		// the holder on the Windows -race runner (the lease itself was never
		// leaked). Only the moment the lease looks free costs a writer.
		if pid, held := sharedLeaseHeld(s, "SHARED"); held {
			if time.Now().After(deadline) {
				res.note("acquire", start, busyHeld(pid))
				return 0
			}
			runtime.Gosched()
			continue
		}
		_, err := s.AcquireLease(ctx, "SHARED")
		switch {
		case errors.Is(err, ErrSessionBusy):
			if time.Now().After(deadline) {
				res.note("acquire", start, err)
				return 0
			}
			runtime.Gosched()
			continue
		case err != nil:
			res.note("acquire", start, err)
			return 0
		}
		n := 0
		_, err = s.Append(ctx, "SHARED", nil,
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
}

// sharedLeaseHeld reports a live foreign owner of sid from a read connection,
// mirroring the busy branch of Tx.AcquireLease so a waiting contender never
// takes the writer lock.
func sharedLeaseHeld(s *Store, sid string) (int, bool) {
	var x liveRec
	var old int64
	if err := s.get(context.Background(), s.q["lease.get"], []any{sid},
		&old, &x.PID, &x.Start, &x.Host, &x.BootID, &x.HeartbeatAt, &x.ExpiresAt); err != nil {
		return 0, false
	}
	if mine, _ := s.token(sid); mine == old || x.Ident == s.id || s.takeoverOK("lease:"+sid, x, false) {
		return 0, false
	}
	return x.PID, true
}

// busyHeld is the failure the lease wait reports when its budget runs out.
func busyHeld(pid int) error {
	return fmt.Errorf("ctxdb: session SHARED held by pid %d: %w", pid, ErrSessionBusy)
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
