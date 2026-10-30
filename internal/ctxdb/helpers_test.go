package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// templateEnv carries the migrated schema template that TestMain builds: newEnv
// copies it instead of every test running the migration ladder. Under -race a
// first Open costs ~1 s (Linux 0.9 s, macos-15-intel 1.06 s p50) and the ladder
// is ~57 % of it; the 86 scenario chain pays that per scenario. The template is
// created once per test binary and removed after m.Run.
const templateEnv = "CTXDB_TEST_TEMPLATE"

func TestMain(m *testing.M) {
	// Re-executed child processes (TestChildProcess) open the data dir their
	// parent passes and never use newEnv, so building the schema template there
	// only repeats the whole migration ladder before the child can say READY.
	// That cost is what made the Windows CI children miss the readiness
	// deadline; the parent's template is inherited through the environment.
	if os.Getenv(childRoleEnv) == "" {
		buildTestTemplate()
	}
	goleak.VerifyTestMain(m)
}

// buildTestTemplate migrates one template dir for newEnv; a failure only makes
// the suite slower and is loud on stderr (no panic/os.Exit: E-23 and forbidigo).
func buildTestTemplate() {
	// A template from an earlier run is removed here, not after m.Run:
	// goleak.VerifyTestMain (E-29) exits the process. Older than an hour only,
	// so a concurrent invocation of this package keeps its own template.
	if stale, err := filepath.Glob(filepath.Join(os.TempDir(), "ctxdb-schema-template-*")); err == nil {
		for _, dir := range stale {
			if fi, serr := os.Stat(dir); serr == nil && time.Since(fi.ModTime()) > time.Hour {
				if rerr := os.RemoveAll(dir); rerr != nil {
					fmt.Fprintf(os.Stderr, "ctxdb: stale schema template: %v\n", rerr)
				}
			}
		}
	}
	dir, err := os.MkdirTemp("", "ctxdb-schema-template-*")
	if err == nil {
		err = buildSchemaTemplate(dir)
	}
	if err == nil {
		err = os.Setenv(templateEnv, dir)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxdb: schema template not built, scenarios will run migrations: %v\n", err)
	}
}

// buildSchemaTemplate applies every migration in dir, then closes the store so
// no holder registration or open handle is left in the template.
func buildSchemaTemplate(dir string) error {
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "serve", BusyTimeout: 2 * time.Second})
	if err != nil {
		return err
	}
	return s.Close()
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
	return wrap(ctx.Err())
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

// newEnv creates a data dir. Its leak check is registered right after
// TempDir, so it runs after every later cleanup (Store.Close, child kill)
// and before the directory is removed: a handle still open there would
// make RemoveAll fail on Windows (ADR-090 matrix).
func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvFrom(t, os.Getenv(templateEnv))
}

// newRawEnv is newEnv without the schema template: for tests that must start
// from an empty data dir (first open, migration, user_version checks).
func newRawEnv(t *testing.T) *env {
	t.Helper()
	return newEnvFrom(t, "")
}

// newEnvFrom creates a data dir, seeded with a copy of template when it is set.
func newEnvFrom(t *testing.T, template string) *env {
	t.Helper()
	dir := t.TempDir()
	if template != "" {
		if err := os.CopyFS(dir, os.DirFS(template)); err != nil {
			t.Fatalf("schema template copy: %v", err)
		}
	}
	t.Cleanup(func() {
		if open := openUnder(dir); len(open) > 0 {
			t.Errorf("handle leak under data dir after cleanup (Windows cannot delete open files): %v", open)
		}
	})
	return &env{t: t, dir: dir, clk: newFakeClock(), procs: &fakeProcs{dead: map[int]bool{}}, boot: "boot-1"}
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
	s, err := Open(context.Background(), &o)
	if err != nil {
		e.t.Fatalf("open pid %d: %v", pid, err)
	}
	e.t.Cleanup(func() { must(e.t, s.Close()) })
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

// session creates a session with lease held by s.
func session(t *testing.T, s *Store, id, parent string) {
	t.Helper()
	_, err := s.CreateSession(context.Background(), Session{ID: id, ParentID: parent, Cwd: "/w"})
	must(t, err)
}

func appendMsg(t *testing.T, s *Store, sid, text string) int64 {
	t.Helper()
	ids, err := s.Append(context.Background(), sid, nil, Event{Kind: "user_message", Role: "user", Content: text})
	must(t, err)
	return ids[0]
}

// checkIntegrity asserts integrity_check, FK check and hash chains.
func checkIntegrity(t *testing.T, s *Store) {
	t.Helper()
	res, err := s.IntegrityCheck(context.Background())
	must(t, err)
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
	rows, err := s.testRows(context.Background(), "SELECT session_id, seq, kind, content, payload, coalesce(blob_sha,''), prev_hash, hash FROM events ORDER BY session_id, seq")
	must(t, err)
	defer func() { must(t, rows.Close()) }()
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

// execAll runs statements in order with the same arguments.
func (t *Tx) execAll(args []any, qs ...string) error {
	for _, q := range qs {
		if _, err := t.Exec(q, args...); err != nil {
			return err
		}
	}
	return nil
}

// testRows runs a read query on a reserved read connection (test helper); the
// connection is released when the returned rows are closed.
func (s *Store) testRows(ctx context.Context, q string, args ...any) (*pinnedRows, error) {
	s.mu.Lock()
	if !s.opt.ReadOnly {
		if err := s.preWrite(ctx); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	if s.r == nil {
		s.mu.Unlock()
		return nil, wrap(sql.ErrConnDone)
	}
	c, err := s.r.Conn(ctx)
	s.mu.Unlock()
	if err != nil {
		return nil, wrap(err)
	}
	rows, err := c.QueryContext(ctx, q, args...) //nolint:rowserrcheck // rows are returned; verifyChains checks rows.Err()
	if err != nil {
		return nil, errors.Join(wrap(err), wrap(c.Close()))
	}
	return &pinnedRows{Rows: rows, conn: c}, nil
}

type pinnedRows struct {
	*sql.Rows
	once sync.Once
	conn *sql.Conn
}

func (r *pinnedRows) Close() (err error) {
	err = r.Rows.Close()
	r.once.Do(func() { err = errors.Join(err, wrap(r.conn.Close())) })
	return err
}

// testBlobPath is the on-disk path of a blob (test helper).
func (s *Store) testBlobPath(sha string) string { return filepath.Join(s.dir, blobName(sha)) }

// testRoot opens the data dir as an os.Root (test helper).
func testRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	must(t, err)
	t.Cleanup(func() { must(t, r.Close()) })
	return r
}

// testLock takes a non-blocking flock on a barrier file (test helper).
func testLock(t *testing.T, dir, name string, exclusive bool) (*os.File, bool) {
	t.Helper()
	f, err := tryLock(testRoot(t, dir), name, exclusive)
	return f, err == nil
}

// runPath is the absolute path of a run/ file (test helper).
func runPath(dir, name string) string { return filepath.Join(dir, "run", name) }
