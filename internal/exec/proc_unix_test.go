//go:build unix

package exec

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func startSh(t *testing.T, script string, tty bool, in *os.File) *osProc {
	t.Helper()
	p, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	s := &Spec{Argv: []string{p, "-c", script}, Dir: t.TempDir(), Env: []string{"PATH=" + os.Getenv("PATH")}, PTY: tty}
	u, err := start(s, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := u.release(); err != nil {
			t.Error(err)
		}
	})
	return u
}

// readAll reads the output until EOF (pipe) or EIO (PTY hang-up).
func readAll(u *osProc) string {
	b, _ := io.ReadAll(u.output()) //nolint:errcheck // EIO ends a PTY stream
	return strings.ReplaceAll(string(b), "\r", "")
}

// waitGone polls until the whole group is gone (leader reaped first).
func waitGone(t *testing.T, u *osProc) {
	t.Helper()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for u.treeAlive() {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("process group still alive")
		}
	}
}

func TestProcPipeExit(t *testing.T) {
	t.Parallel()
	u := startSh(t, `echo out; echo err >&2; test -t 0 || echo notty; exit 4`, false, nil)
	out := readAll(u)
	ex, err := u.wait()
	if err != nil || ex.code != 4 || ex.signal != "" {
		t.Fatalf("%+v %v", ex, err)
	}
	if out != "out\nerr\nnotty\n" {
		t.Fatalf("%q", out)
	}
	waitGone(t, u)
	if err := u.closeOutput(); err != nil {
		t.Fatal(err)
	}
}

func TestProcStdinPipe(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	u := startSh(t, `cat`, false, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("in"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if out := readAll(u); out != "in" {
		t.Fatalf("%q", out)
	}
	if _, err := u.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProcGroupTerminate(t *testing.T) {
	t.Parallel()
	u := startSh(t, `sleep 100 & (sleep 100 &); echo up; wait`, false, nil)
	buf := make([]byte, 3)
	if _, err := io.ReadFull(u.output(), buf); err != nil {
		t.Fatal(err)
	}
	if err := u.terminate(); err != nil {
		t.Fatal(err)
	}
	ex, err := u.wait()
	if err != nil || ex.signal != "SIGTERM" {
		t.Fatalf("%+v %v", ex, err)
	}
	waitGone(t, u)
	if err := u.kill(); err != nil { // ESRCH on an empty group is not an error
		t.Fatal(err)
	}
}

func TestProcGroupKillIgnoresTrap(t *testing.T) {
	t.Parallel()
	u := startSh(t, `trap '' TERM; echo up; while :; do sleep 1; done`, false, nil)
	buf := make([]byte, 3)
	if _, err := io.ReadFull(u.output(), buf); err != nil {
		t.Fatal(err)
	}
	if err := u.kill(); err != nil {
		t.Fatal(err)
	}
	if ex, err := u.wait(); err != nil || ex.signal != "SIGKILL" {
		t.Fatalf("%+v %v", ex, err)
	}
	waitGone(t, u)
}

func TestProcPTY(t *testing.T) {
	t.Parallel()
	u := startSh(t, `test -t 0 && test -t 1 && echo tty; stty size; (: </dev/tty) && echo ctty`, true, nil)
	out := readAll(u)
	if _, err := u.wait(); err != nil {
		t.Fatal(err)
	}
	if out != "tty\n40 120\nctty\n" {
		t.Fatalf("%q", out)
	}
}

// TestProcPTYCloseUnblocksRead (EX39-1): the PTY master must be
// non-blocking and poller-backed, so closing it interrupts a pending read
// instead of hanging Run and Runner.Close. A blocking master (File.Fd
// resets the flag) would never wake the read.
func TestProcPTYCloseUnblocksRead(t *testing.T) {
	t.Parallel()
	u := startSh(t, `exec sleep 30`, true, nil)
	conn, err := u.output().SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	if cerr := conn.Control(func(fd uintptr) {
		fl, ferr := unix.FcntlInt(fd, syscall.F_GETFL, 0)
		if ferr != nil {
			t.Errorf("fcntl(F_GETFL): %v", ferr)
			return
		}
		flags = fl
	}); cerr != nil {
		t.Fatal(cerr)
	}
	if flags&syscall.O_NONBLOCK == 0 {
		t.Fatalf("PTY master is blocking (flags %#x); Close cannot interrupt Read", flags)
	}
	done := make(chan error, 1)
	go func() {
		_, rerr := u.output().Read(make([]byte, 1))
		done <- rerr
	}()
	if err := u.closeOutput(); err != nil {
		t.Fatal(err)
	}
	select {
	case rerr := <-done:
		if !errors.Is(rerr, os.ErrClosed) {
			t.Fatalf("Read after Close: %v, want os.ErrClosed", rerr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read still blocked after Close (EX39-1)")
	}
	if err := u.kill(); err != nil {
		t.Fatal(err)
	}
	if _, err := u.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProcStartFails(t *testing.T) {
	t.Parallel()
	for _, tty := range []bool{false, true} {
		if _, err := start(&Spec{Argv: []string{"/nonexistent/x"}, PTY: tty}, nil); err == nil {
			t.Fatal("started a missing binary")
		}
	}
}

func TestSignalName(t *testing.T) {
	t.Parallel()
	if got := signalName(9); got != "SIGKILL" {
		t.Fatal(got)
	}
	if got := signalName(200); got != "SIG200" {
		t.Fatal(got)
	}
}
