package ctxdb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeClock is an injected clock: boot (runs in suspend), mono (stops in
// suspend) and wall (may step). Sleep advances all three instantly.
type fakeClock struct {
	mu               sync.Mutex
	boot, mono, wall int64
}

func newFakeClock() *fakeClock {
	return &fakeClock{boot: 1_000_000, mono: 1_000_000, wall: time.Now().UnixMilli()}
}

func (c *fakeClock) BootMs() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.boot }
func (c *fakeClock) MonoMs() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.mono }
func (c *fakeClock) WallMs() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.wall }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.Advance(d)
	return ctx.Err()
}

// Advance moves every clock by d (normal running time).
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.boot += d.Milliseconds()
	c.mono += d.Milliseconds()
	c.wall += d.Milliseconds()
}

// Suspend models laptop sleep: boot and wall advance, mono does not.
func (c *fakeClock) Suspend(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.boot += d.Milliseconds()
	c.wall += d.Milliseconds()
}

// StepWall steps only the wall clock (NTP / manual change).
func (c *fakeClock) StepWall(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall += d.Milliseconds()
}

// fakeProcs decides which simulated owners are provably gone.
type fakeProcs struct {
	mu   sync.Mutex
	dead map[int]bool
}

func (p *fakeProcs) Gone(id Ident) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead[id.PID]
}

func (p *fakeProcs) kill(pid int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dead[pid] = true
}

// env is one data dir shared by simulated processes (Stores with distinct
// identities on one real SQLite file). Each Store has its own connections,
// flock descriptors and dmb_* functions, exactly like separate processes.
type env struct {
	t     *testing.T
	dir   string
	clk   *fakeClock
	procs *fakeProcs
	boot  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return &env{t: t, dir: t.TempDir(), clk: newFakeClock(), procs: &fakeProcs{dead: map[int]bool{}}, boot: "boot-1"}
}

func (e *env) ident(pid int) *Ident {
	return &Ident{Host: "testhost#4026531836", PID: pid, Start: fmt.Sprint("start-", pid), BootID: e.boot}
}

func (e *env) opts(pid int, kind string) Options {
	return Options{Dir: e.dir, Kind: kind, Clock: e.clk, Procs: e.procs, Ident: e.ident(pid), BusyTimeout: 2 * time.Second}
}

func (e *env) open(pid int, kind string, mods ...func(*Options)) *Store {
	e.t.Helper()
	o := e.opts(pid, kind)
	for _, m := range mods {
		m(&o)
	}
	s, err := Open(context.Background(), o)
	if err != nil {
		e.t.Fatalf("open pid %d: %v", pid, err)
	}
	e.t.Cleanup(func() { _ = s.Close() })
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("want %v, got %v", target, err)
	}
}

func count(t *testing.T, s *Store, q string, args ...any) int64 {
	t.Helper()
	n, err := s.Count(context.Background(), q, args...)
	must(t, err)
	return n
}

var bg = context.Background()

// session creates a session with lease held by s.
func session(t *testing.T, s *Store, id, parent string) {
	t.Helper()
	_, err := s.CreateSession(bg, Session{ID: id, ParentID: parent, Cwd: "/w"})
	must(t, err)
}

func appendMsg(t *testing.T, s *Store, sid, text string) int64 {
	t.Helper()
	ids, err := s.Append(bg, sid, nil, Event{Kind: "user_message", Role: "user", Content: text})
	must(t, err)
	return ids[0]
}

// checkIntegrity asserts integrity_check, FK check and hash chains.
func checkIntegrity(t *testing.T, s *Store) {
	t.Helper()
	var res string
	must(t, s.get(bg, "PRAGMA integrity_check", nil, &res))
	if res != "ok" {
		t.Fatalf("integrity_check: %s", res)
	}
	if n := count(t, s, "SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Fatalf("foreign_key_check: %d violations", n)
	}
	if bad := verifyChains(t, s); bad != "" {
		t.Fatalf("hash chain broken: %s", bad)
	}
}

func verifyChains(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.query(bg, "SELECT session_id, seq, kind, content, payload, coalesce(blob_sha,''), prev_hash, hash FROM events ORDER BY session_id, seq")
	must(t, err)
	defer rows.Close()
	prev, cur := "", ""
	for rows.Next() {
		var sid, kind, content, payload, blob, ph, h string
		var seq int64
		must(t, rows.Scan(&sid, &seq, &kind, &content, &payload, &blob, &ph, &h))
		if sid != cur {
			cur, prev = sid, ""
		}
		if ph != prev || h != EventHash(prev, sid, seq, kind, content, payload, blob) {
			return fmt.Sprintf("%s#%d", sid, seq)
		}
		prev = h
	}
	must(t, rows.Err())
	return ""
}
