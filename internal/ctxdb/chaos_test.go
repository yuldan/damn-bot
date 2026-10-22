package ctxdb

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

const (
	childRoleEnv = "CTXDB_CHILD_ROLE"
	childDirEnv  = "CTXDB_CHILD_DIR"
)

func goexit() { runtime.Goexit() }

// rnd returns a uniform int in [0, n) (crypto/rand: chaos needs no replay).
func rnd(t *testing.T, n int) int {
	t.Helper()
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	must(t, err)
	return int(v.Int64())
}

// say writes one protocol line to the parent process.
func say(t *testing.T, line string) {
	t.Helper()
	_, err := os.Stdout.WriteString(line + "\n")
	must(t, err)
}

// waitStdin blocks until the parent closes stdin or kills the child.
func waitStdin() error {
	_, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return wrap(err)
}

// TestChildProcess is the entry point of re-executed test binaries (real
// OS processes for kill -9 / SIGSTOP / multi-process scenarios). Without
// the role variable it has nothing to do.
func TestChildProcess(t *testing.T) {
	role, dir := os.Getenv(childRoleEnv), os.Getenv(childDirEnv)
	switch role {
	case "":
		return
	case "barrier":
		b, err := AcquireBarrier(context.Background(), dir, SelfIdent(), reasonRestore, 30, BarrierWait)
		must(t, err)
		say(t, "READY")
		t.Log(waitStdin()) // EOF/killed: either way the barrier is released below
		must(t, b.Release())
		return
	case "hold-fd":
		// EC-STATE-016: keep an inherited descriptor (flock) alive while the
		// parent drops its own copy.
		say(t, "READY")
		t.Log(waitStdin())
		return
	}
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "tui"})
	must(t, err)
	defer func() { must(t, s.Close()) }()
	switch role {
	case "hold-tx":
		must(t, s.Write(context.Background(), func(*Tx) error {
			say(t, "READY")
			t.Log(waitStdin())
			return nil
		}))
	case "chaos":
		say(t, "READY")
		chaosLoop(t, s, 1<<30)
	case "turn":
		// A turn interrupted by SIGKILL: the parent asserts the recovery
		// marker (GAP-REL-01).
		if _, err := s.CreateSession(context.Background(), Session{ID: "crash", Origin: "tui", Cwd: "/"}); err != nil {
			must(t, err)
		}
		must(t, s.Write(context.Background(), func(tx *Tx) error {
			if err := tx.StartTurn("crash", "T1"); err != nil {
				return err
			}
			if _, err := tx.AppendEvent("crash", &Event{Kind: "user_message", TurnID: "T1", Role: "user", Content: "poison"}); err != nil {
				return err
			}
			_, err := tx.AppendEvent("crash", &Event{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "partial"})
			return err
		}))
		say(t, "READY")
		t.Log(waitStdin())
	case "multi":
		say(t, "READY")
		b, err := json.Marshal(multiWorker(t, s, 150))
		must(t, err)
		say(t, "RESULT "+string(b))
	}
}

type child struct {
	cmd    *exec.Cmd
	out    *bufio.Scanner
	stderr *syncBuffer
}

// syncBuffer collects child stderr; written by os/exec's copier goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := s.b.Write(p) // strings.Builder.Write never returns an error
	return n, nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func startChild(t *testing.T, role, dir string) *child {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestChildProcess$", "-test.count=1") //nolint:gosec // G204: re-exec of this test binary
	cmd.Env = append(os.Environ(), childRoleEnv+"="+role, childDirEnv+"="+dir)
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	stdin, err := cmd.StdinPipe()
	must(t, err)
	c := &child{cmd: cmd, out: bufio.NewScanner(stdout), stderr: &syncBuffer{}}
	cmd.Stderr = c.stderr
	must(t, cmd.Start())
	t.Cleanup(func() {
		t.Log(stdin.Close())
		c.kill9(t)
	})
	// The child's test framework writes its failure (t.Fatal output) to stdout before
	// READY; keep those lines so an early exit is diagnosable instead of opaque.
	type ready struct {
		ok  bool
		pre []string
	}
	ch := make(chan ready, 1)
	go func() {
		var pre []string
		for c.out.Scan() {
			if c.out.Text() == "READY" {
				ch <- ready{ok: true}
				return
			}
			pre = append(pre, c.out.Text())
		}
		ch <- ready{pre: pre}
	}()
	select {
	case r := <-ch:
		if !r.ok {
			werr := cmd.Wait()
			t.Fatalf("child %s exited before READY: %v\nstdout:\n%s\nstderr:\n%s",
				role, werr, strings.Join(r.pre, "\n"), c.stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("child %s not ready", role)
	}
	return c
}

// kill9 SIGKILLs the child and reaps it; "already finished" and the
// "signal: killed" exit status are the expected outcomes. A kill error is
// only real if the child is still running afterwards: Windows reports a
// child that exited on its own (TerminateProcess → "Access is denied") or
// was already reaped ("invalid argument") as an error.
func (c *child) kill9(t *testing.T) {
	t.Helper()
	kerr := c.cmd.Process.Kill()
	var ee *exec.ExitError
	werr := c.cmd.Wait()
	reaped := werr == nil || errors.As(werr, &ee) || strings.Contains(werr.Error(), "Wait was already called")
	if !reaped {
		t.Fatal(errors.Join(kerr, werr))
	}
}

// result reads the child's RESULT line and waits for a clean exit.
func (c *child) result(t *testing.T) multiResult {
	t.Helper()
	var r multiResult
	for c.out.Scan() {
		if line, ok := strings.CutPrefix(c.out.Text(), "RESULT "); ok {
			must(t, json.Unmarshal([]byte(line), &r))
		}
	}
	must(t, c.cmd.Wait())
	return r
}

// expected reports the closed set of outcomes a random chaos step may
// legitimately hit (races with other steps); anything else fails the run.
func expected(err error) bool {
	for _, e := range []error{
		ErrSessionPurging, ErrLeaseFenced, ErrNotFound, ErrSessionBusy,
		ErrPurgeInProgress, ErrHasForks, ErrBlobExpired, ErrPurgePending,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return err == nil
}

func check(t *testing.T, step string, err error) {
	t.Helper()
	if !expected(err) {
		t.Fatalf("chaos %s: %v", step, err)
	}
}

// chaosLoop mixes every writer path: sessions, forks, events with shared
// blobs, journals, inbound, purge, gc — until killed.
func chaosLoop(t *testing.T, s *Store, iters int) {
	for i := range iters {
		sid := fmt.Sprintf("c%d-%d", os.Getpid(), i)
		ids, err := s.ListSessions(context.Background())
		must(t, err)
		parent := ""
		if len(ids) > 0 && rnd(t, 3) == 0 {
			parent = ids[rnd(t, len(ids))]
		}
		if _, cerr := s.CreateSession(context.Background(), Session{ID: sid, ParentID: parent, Origin: "delegate"}); cerr != nil {
			check(t, "create", cerr)
			continue
		}
		chaosTurn(t, s, sid)
		switch rnd(t, 6) {
		case 0:
			if len(ids) > 0 {
				_, err = s.Purge(context.Background(), PurgeRequest{IDs: []string{ids[rnd(t, len(ids))]}, WithForks: true})
				check(t, "purge", err)
			}
		case 1:
			_, err = s.GC(context.Background(), false)
			check(t, "gc", err)
		case 2:
			check(t, "resume", s.ResumePurge(context.Background(), true))
		}
	}
}

func chaosTurn(t *testing.T, s *Store, sid string) {
	t.Helper()
	data := []byte("shared-blob")
	if rnd(t, 2) == 0 {
		data = []byte(sid)
	}
	pb, err := s.PutBlob(data, "text/plain")
	must(t, err)
	_, err = s.Append(context.Background(), sid, []*PendingBlob{pb}, Event{Kind: "tool_result", Blob: pb.Sha}, Event{Kind: "user_message", Content: sid})
	check(t, "append", err)
	check(t, "turn_start", s.Write(context.Background(), func(tx *Tx) error { return tx.StartTurn(sid, "t"+sid) }))
	if rnd(t, 2) == 0 {
		check(t, "turn_end", s.Write(context.Background(), func(tx *Tx) error { return tx.EndTurn(sid, "t"+sid, "end_turn") }))
		check(t, "release", s.ReleaseLease(context.Background(), sid))
	}
	check(t, "ingest", s.IngestInbound(context.Background(), []Inbound{inbound(sid, "chat", "hi")}, "", ""))
	_, err = s.ProcessInbound(context.Background(), "telegram", "chat")
	check(t, "worker", err)
}

// TestKill9Chaos SIGKILLs a real writer process at random points, then
// verifies recovery: integrity, FK, hash chains, I9, I10 (pending purges
// complete), I3 (no live writes into labelled sessions after resume).
func TestKill9Chaos(t *testing.T) {
	dir := t.TempDir()
	const rounds = 15
	for round := range rounds {
		c := startChild(t, "chaos", dir)
		select {
		case <-time.After(time.Duration(50+rnd(t, 400)) * time.Millisecond):
		case <-t.Context().Done():
		}
		c.kill9(t)
		pre := rawIntegrity(t, dir, fmt.Sprintf("%d-pre", round)) // state at kill time, before any recovery
		s, err := Open(context.Background(), &Options{Dir: dir, Kind: "serve"})
		must(t, err)
		if pre != "ok" {
			t.Fatalf("round %d: corrupt at kill time (before recovery): %s", round, pre)
		}
		checkIntegrity(t, s)
		assertLiveBlobsHaveFiles(t, s)
		if _, err := s.RecoverJournals(context.Background(), false); err != nil {
			t.Fatalf("round %d recovery: %v", round, err)
		}
		if err := s.ResumePurge(context.Background(), false); err != nil {
			t.Fatalf("round %d resume purge: %v", round, err)
		}
		if n := count(t, s, "SELECT count(*) FROM module_state WHERE module_id = 'purge'"); n != 0 {
			t.Fatalf("round %d: purge left pending", round)
		}
		checkIntegrity(t, s)
		must(t, s.Close())
	}
}

// rawIntegrity copies the DB (+WAL) to /tmp/ts-p11-<tag>/ and runs
// integrity_check on the copy through a plain driver connection (no ctxdb
// functions, no recovery), so evidence survives and recovery cannot hide it.
func rawIntegrity(t *testing.T, dir, tag string) string {
	t.Helper()
	dst := filepath.Join(os.TempDir(), "ts-p11-"+tag)
	must(t, os.RemoveAll(dst))
	must(t, os.CopyFS(dst, os.DirFS(dir))) // whole data dir: db, -wal, -shm, blobs, run/
	d := &sqlite.Driver{}
	db := sql.OpenDB(connector{d: d, dsn: "file:" + filepath.Join(dst, dbFile)})
	var res string
	qerr := db.QueryRowContext(context.Background(), "PRAGMA integrity_check").Scan(&res)
	must(t, errors.Join(qerr, db.Close())) // closed before removal: Windows cannot delete an open DB
	if res == "ok" {
		must(t, os.RemoveAll(dst)) // keep the copy only as evidence of a failure
	}
	return res
}

type multiResult struct {
	Own, Shared, Busy int
	Errors            []string
}

func (res *multiResult) note(op string, start time.Time, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, ErrDBBusy) {
		res.Busy++
	}
	res.Errors = append(res.Errors, fmt.Sprintf("%s after %v: %v", op, time.Since(start), err))
}

// multiWorker: own sessions + contention on one shared session + purge/gc.
func multiWorker(t *testing.T, s *Store, n int) multiResult {
	var res multiResult
	own := fmt.Sprintf("own-%d", os.Getpid())
	_, err := s.CreateSession(context.Background(), Session{ID: own})
	res.note("create", time.Now(), err)
	for range n {
		start := time.Now()
		if _, aerr := s.Append(context.Background(), own, nil, Event{Kind: "user_message", Content: "x"}); aerr != nil {
			res.note("append", start, aerr)
		} else {
			res.Own++
		}
		res.sharedStep(s, own)
		if rnd(t, 10) < 2 {
			res.maintStep(s, rnd(t, 2) == 0)
		}
	}
	return res
}

func (res *multiResult) sharedStep(s *Store, own string) {
	start := time.Now()
	_, err := s.AcquireLease(context.Background(), "SHARED")
	if errors.Is(err, ErrSessionBusy) {
		return
	}
	if err != nil {
		res.note("acquire", start, err)
		return
	}
	if _, err = s.Append(context.Background(), "SHARED", nil, Event{Kind: "user_message", Content: own}); err == nil {
		res.Shared++
	}
	res.note("shared append", start, err)
	res.note("release", start, s.ReleaseLease(context.Background(), "SHARED"))
}

func (res *multiResult) maintStep(s *Store, gc bool) {
	start := time.Now()
	if gc {
		_, err := s.GC(context.Background(), false)
		res.note("gc", start, err)
		return
	}
	tmp := "tmp-" + newID()
	_, err := s.CreateSession(context.Background(), Session{ID: tmp})
	if err == nil {
		err = s.ReleaseLease(context.Background(), tmp)
	}
	if err == nil {
		if _, err = s.Purge(context.Background(), PurgeRequest{IDs: []string{tmp}}); errors.Is(err, ErrPurgeInProgress) {
			err = nil
		}
	}
	res.note("purge", start, err)
}

// TestMultiProcess: four OS processes on one DB. Every append is accounted
// for, the shared session's chain is intact, 0 db_busy reached a caller.
func TestMultiProcess(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "serve"})
	must(t, err)
	_, err = s.CreateSession(context.Background(), Session{ID: "SHARED"})
	must(t, err)
	must(t, s.ReleaseLease(context.Background(), "SHARED"))
	must(t, s.Close())
	var kids []*child
	for range 4 {
		kids = append(kids, startChild(t, "multi", dir))
	}
	shared := 0
	for _, c := range kids {
		res := c.result(t)
		if res.Busy != 0 || len(res.Errors) != 0 {
			t.Fatalf("child errors: busy=%d %v", res.Busy, res.Errors)
		}
		if res.Own != 150 {
			t.Fatalf("own appends %d", res.Own)
		}
		shared += res.Shared
	}
	s, err = Open(context.Background(), &Options{Dir: dir, Kind: "serve"})
	must(t, err)
	defer func() { must(t, s.Close()) }()
	checkIntegrity(t, s)
	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id = 'SHARED'"); int(n) != shared {
		t.Fatalf("shared events %d, reported %d", n, shared)
	}
	if n := count(t, s, "SELECT count(*) FROM module_state WHERE module_id = 'purge'"); n != 0 {
		t.Fatal("purge left pending after clean exits")
	}
}

// TestChaos50Clients (GAP-REL-03): 50 concurrent CLI-shaped processes for a
// configurable window (short in PRs, 30m in nightly) never surface
// "database is locked" and leave integrity_check ok.
func TestChaos50Clients(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos")
	}
	dur := 2 * time.Second
	if v := os.Getenv("DAMNBOT_CHAOS_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("DAMNBOT_CHAOS_DURATION=%q: %v", v, err)
		}
		dur = d
	}
	dir := t.TempDir()
	deadline := time.Now().Add(dur)
	errs := make(chan error, 50)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			ctx := context.Background()
			id := &Ident{Host: "chaos", PID: i + 1, Start: "chaos", BootID: "chaos"}
			s, err := Open(ctx, &Options{Dir: dir, Kind: "cli", Ident: id, BusyTimeout: 5 * time.Second})
			if err != nil {
				errs <- err
				return
			}
			sid := "C" + itoa(int64(i))
			if _, err := s.CreateSession(ctx, Session{ID: sid, Cwd: "/"}); err != nil && !errors.Is(err, ErrIdempotencyConflict) {
				errs <- err
				_ = s.Close()
				return
			}
			for n := 0; time.Now().Before(deadline); n++ {
				if _, err := s.Append(ctx, sid, nil, Event{Kind: "user_message", Content: "chaos " + itoa(int64(n))}); err != nil {
					errs <- err
					break
				}
				if _, err := s.Recall(ctx, "chaos", 5); err != nil {
					errs <- err
					break
				}
			}
			_ = s.Close()
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("chaos client: %v", err)
		}
	}
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, s.Close()) }()
	if res, err := s.IntegrityCheck(context.Background()); err != nil || res != "ok" {
		t.Fatalf("integrity = %q, %v", res, err)
	}
}
