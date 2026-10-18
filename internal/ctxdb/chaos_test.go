package ctxdb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	childRoleEnv = "CTXDB_CHILD_ROLE"
	childDirEnv  = "CTXDB_CHILD_DIR"
	sigStop      = syscall.SIGSTOP
	sigCont      = syscall.SIGCONT
)

func goexit() { runtime.Goexit() }

// TestChildProcess is the entry point of re-executed test binaries (real
// OS processes for kill -9 / SIGSTOP / multi-process scenarios). Without
// the role variable it has nothing to do.
func TestChildProcess(t *testing.T) {
	role := os.Getenv(childRoleEnv)
	if role == "" {
		return
	}
	dir := os.Getenv(childDirEnv)
	ctx := context.Background()
	if role == "barrier" {
		b, err := AcquireBarrier(ctx, dir, SelfIdent(), reasonRestore, 30, BarrierWait)
		must(t, err)
		defer b.Release()
		fmt.Println("READY")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // block until killed
		return
	}
	s, err := Open(ctx, Options{Dir: dir, Kind: "tui"})
	must(t, err)
	defer func() { must(t, s.Close()) }()
	switch role {
	case "hold-tx":
		_ = s.Write(ctx, func(*Tx) error {
			fmt.Println("READY")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			return nil
		})
	case "chaos":
		fmt.Println("READY")
		chaosLoop(t, s, rand.New(rand.NewPCG(uint64(os.Getpid()), 7)), 1<<30)
	case "multi":
		fmt.Println("READY")
		res := multiWorker(t, s, 150)
		b, _ := json.Marshal(res)
		fmt.Println("RESULT " + string(b))
	}
}

type child struct {
	cmd *exec.Cmd
	out *bufio.Scanner
}

func startChild(t *testing.T, role, dir string) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), childRoleEnv+"="+role, childDirEnv+"="+dir)
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	stdin, err := cmd.StdinPipe()
	must(t, err)
	t.Cleanup(func() { _ = stdin.Close() })
	must(t, cmd.Start())
	c := &child{cmd: cmd, out: bufio.NewScanner(stdout)}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan bool, 1)
	go func() {
		for c.out.Scan() {
			if c.out.Text() == "READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatalf("child %s exited before READY", role)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("child %s not ready", role)
	}
	return c
}

func (c *child) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	must(t, c.cmd.Process.Signal(sig))
}

func (c *child) kill9(t *testing.T) {
	t.Helper()
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
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

// chaosLoop mixes every writer path: sessions, forks, events with shared
// blobs, journals, inbound, purge, gc — until killed.
func chaosLoop(t *testing.T, s *Store, r *rand.Rand, iters int) {
	shared := []byte("shared-blob")
	for i := range iters {
		sid := fmt.Sprintf("c%d-%d", os.Getpid(), i)
		parent := ""
		if ids, _ := s.ListSessions(bg); len(ids) > 0 && r.IntN(3) == 0 {
			parent = ids[r.IntN(len(ids))]
		}
		if _, err := s.CreateSession(bg, Session{ID: sid, ParentID: parent, Origin: "delegate"}); err != nil {
			continue
		}
		data := shared
		if r.IntN(2) == 0 {
			data = []byte(sid)
		}
		if pb, err := s.PutBlob(data, "text/plain"); err == nil {
			_, _ = s.Append(bg, sid, []*PendingBlob{pb}, Event{Kind: "tool_result", Blob: pb.Sha}, Event{Kind: "user_message", Content: sid})
		}
		_ = s.Write(bg, func(tx *Tx) error { return tx.StartTurn(sid, "t"+sid) })
		if r.IntN(2) == 0 {
			_ = s.Write(bg, func(tx *Tx) error { return tx.EndTurn(sid, "t"+sid, "end_turn") })
			_ = s.ReleaseLease(bg, sid)
		}
		_ = s.IngestInbound(bg, []Inbound{inbound(sid, "chat", "hi")}, "", "")
		_, _ = s.ProcessInbound(bg, "telegram", "chat")
		switch r.IntN(6) {
		case 0:
			if ids, _ := s.ListSessions(bg); len(ids) > 0 {
				_, _ = s.Purge(bg, PurgeRequest{IDs: []string{ids[r.IntN(len(ids))]}, WithForks: true})
			}
		case 1:
			_, _ = s.GC(bg, false)
		case 2:
			_ = s.ResumePurge(bg, true)
		}
	}
	_ = t
}

// TestKill9Chaos SIGKILLs a real writer process at random points, then
// verifies recovery: integrity, FK, hash chains, I9, I10 (pending purges
// complete), I3 (no live writes into labelled sessions after resume).
func TestKill9Chaos(t *testing.T) {
	dir := t.TempDir()
	r := rand.New(rand.NewPCG(42, 1))
	const rounds = 15
	for round := range rounds {
		c := startChild(t, "chaos", dir)
		select {
		case <-time.After(time.Duration(50+r.IntN(400)) * time.Millisecond):
		case <-t.Context().Done():
		}
		c.kill9(t)
		s, err := Open(bg, Options{Dir: dir, Kind: "serve"})
		must(t, err)
		checkIntegrity(t, s)
		assertLiveBlobsHaveFiles(t, s)
		if _, err := s.RecoverJournals(bg, false); err != nil {
			t.Fatalf("round %d recovery: %v", round, err)
		}
		if err := s.ResumePurge(bg, false); err != nil && !isBusy(err) {
			t.Fatalf("round %d resume purge: %v", round, err)
		}
		if n := count(t, s, "SELECT count(*) FROM module_state WHERE module_id = 'purge'"); n != 0 {
			t.Fatalf("round %d: purge left pending", round)
		}
		checkIntegrity(t, s)
		must(t, s.Close())
	}
}

func isBusy(err error) bool { return errors.Is(err, ErrDBBusy) }

type multiResult struct {
	Own, Shared, Busy int
	Errors            []string
}

// multiWorker: own sessions + contention on one shared session + purge/gc.
func multiWorker(t *testing.T, s *Store, n int) multiResult {
	var res multiResult
	r := rand.New(rand.NewPCG(uint64(os.Getpid()), 3))
	start := time.Now()
	note := func(op string, err error) {
		if err != nil {
			if isBusy(err) {
				res.Busy++
			}
			res.Errors = append(res.Errors, fmt.Sprintf("%s after %v: %v", op, time.Since(start), err))
		}
	}
	own := fmt.Sprintf("own-%d", os.Getpid())
	_, err := s.CreateSession(bg, Session{ID: own})
	note("create", err)
	for range n {
		start = time.Now()
		if _, err := s.Append(bg, own, nil, Event{Kind: "user_message", Content: "x"}); err == nil {
			res.Own++
		} else {
			note("append", err)
		}
		start = time.Now()
		if _, err := s.AcquireLease(bg, "SHARED"); err == nil {
			if _, err := s.Append(bg, "SHARED", nil, Event{Kind: "user_message", Content: own}); err == nil {
				res.Shared++
			}
			start = time.Now()
			note("release", s.ReleaseLease(bg, "SHARED"))
		} else if !errors.Is(err, ErrSessionBusy) {
			note("acquire", err)
		}
		start = time.Now()
		switch r.IntN(10) {
		case 0:
			_, err := s.GC(bg, false)
			note("gc", err)
		case 1:
			tmp := fmt.Sprintf("tmp-%d-%d", os.Getpid(), r.Int())
			if _, err := s.CreateSession(bg, Session{ID: tmp}); err == nil {
				note("release-tmp", s.ReleaseLease(bg, tmp))
				start = time.Now()
				if _, err := s.Purge(bg, PurgeRequest{IDs: []string{tmp}}); err != nil && !errors.Is(err, ErrPurgeInProgress) {
					note("purge", err)
				}
			}
		}
	}
	_ = t
	return res
}

// TestMultiProcess: four OS processes on one DB. Every append is accounted
// for, the shared session's chain is intact, 0 db_busy reached a caller.
func TestMultiProcess(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(bg, Options{Dir: dir, Kind: "serve"})
	must(t, err)
	_, err = s.CreateSession(bg, Session{ID: "SHARED"})
	must(t, err)
	must(t, s.ReleaseLease(bg, "SHARED"))
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
	s, err = Open(bg, Options{Dir: dir, Kind: "serve"})
	must(t, err)
	defer s.Close()
	checkIntegrity(t, s)
	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id = 'SHARED'"); int(n) != shared {
		t.Fatalf("shared events %d, reported %d", n, shared)
	}
	if n := count(t, s, "SELECT count(*) FROM module_state WHERE module_id = 'purge'"); n != 0 {
		t.Fatal("purge left pending after clean exits")
	}
}
