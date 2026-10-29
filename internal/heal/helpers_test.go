package heal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// testClock is the injected clock shared by the supervisor and watchdog tests.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Unix(1_700_000_000, 0)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newStore opens a real data dir (migrations + startup recovery) as serve.
func newStore(t *testing.T) *ctxdb.Store {
	t.Helper()
	s, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "serve", BusyTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s
}

// hangTurn creates a session with an open journal row, then releases the
// session lease as a killed process would.
func hangTurn(t *testing.T, s *ctxdb.Store, sid, turn, class string) {
	t.Helper()
	if _, err := s.CreateSession(t.Context(), ctxdb.Session{ID: sid, Origin: "gateway"}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	err := s.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if err := tx.StartTurn(sid, turn); err != nil {
			return fmt.Errorf("start turn: %w", err)
		}
		if class == "" {
			return nil
		}
		if err := tx.ToolPending(sid, turn, &ctxdb.ToolCall{CallID: "call-1", Name: "shell", Class: class, ArgsSha: "sha-args"}); err != nil {
			return fmt.Errorf("pending tool: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("write turn: %v", err)
	}
	if err := s.ReleaseLease(t.Context(), sid); err != nil {
		t.Fatalf("release lease: %v", err)
	}
}

func count(t *testing.T, s *ctxdb.Store, q string, args ...any) int64 {
	t.Helper()
	n, err := s.Count(t.Context(), q, args...)
	if err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

// step returns the named repair step result.
func step(t *testing.T, r Report, name string) StepResult {
	t.Helper()
	for _, s := range r.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("repair step %q missing in %+v", name, r.Steps)
	return StepResult{}
}

// newWatchdog builds a watchdog with the given clock and action.
func newWatchdog(t *testing.T, clk *testClock, o WatchdogOptions) *Watchdog {
	t.Helper()
	o.Now = clk.Now
	if o.Action == nil {
		o.Action = func(context.Context) error { return nil }
	}
	w, err := NewWatchdog(o)
	if err != nil {
		t.Fatalf("new watchdog: %v", err)
	}
	return w
}

// mustOK fails the test when err is not nil.
func mustOK(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// mustIs fails unless err matches want (errors.Is).
func mustIs(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s = %v, want %v", what, err, want)
	}
}

// readFile reads a file under a fixed root (no variable os.Open in tests).
func readFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root %s: %v", dir, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close root: %v", cerr)
		}
	}()
	b, err := root.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}
