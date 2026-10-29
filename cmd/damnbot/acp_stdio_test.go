//go:build unix || windows

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	isolationHelperEnv = "DAMNBOT_ACP_ISOLATION_HELPER"
	isolationSkipEnv   = "DAMNBOT_ACP_ISOLATION_SKIP"
	isolationDataEnv   = "DAMNBOT_ACP_ISOLATION_DATA"
)

// TestACPChildStdinIsolation (GAP-REL-13, ADR-082 single name): after
// isolateACPStdio the process standard input is /dev/null, so 1.000 child
// spawns that forward os.Stdin cannot consume the JSON-RPC transport; the
// skip-isolation subtest is the negative control and must lose the frame.
func TestACPChildStdinIsolation(t *testing.T) {
	if os.Getenv(isolationHelperEnv) == "1" {
		runIsolationHelper(t)
		return
	}
	t.Run("isolated", func(t *testing.T) {
		runIsolationChild(t, false)
	})
	t.Run("negative-control-no-isolation", func(t *testing.T) {
		runIsolationChild(t, true)
	})
}

// runIsolationChild starts the helper and asserts the protocol frame either
// survives (isolated) or is swallowed by a child (control).
func runIsolationChild(t *testing.T, skip bool) {
	t.Helper()
	h := startIsolationHelper(t, skip)
	if _, err := fmt.Fprintln(h.in, "PING-FRAME"); err != nil {
		t.Fatal(err)
	}
	if err := h.in.Close(); err != nil {
		t.Fatal(err)
	}
	if skip {
		h.expectSwallowed(t)
		return
	}
	h.expectFrame(t)
}

// isolationHelper is one spawned helper process.
type isolationHelper struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	got    chan string
	waitCh chan error
	stderr *strings.Builder
	exited bool
}

// startIsolationHelper spawns the helper and starts its waiter and reader.
func startIsolationHelper(t *testing.T, skip bool) *isolationHelper {
	t.Helper()
	stderr := &strings.Builder{}
	// Struct literal rather than exec.Command: the path is this test binary
	// and G204 has nothing to add there.
	cmd := &exec.Cmd{
		Path: os.Args[0],
		Args: []string{os.Args[0], "-test.run=^TestACPChildStdinIsolation$"},
		Env: append(os.Environ(),
			isolationHelperEnv+"=1",
			fmt.Sprintf("%s=%t", isolationSkipEnv, skip),
			isolationDataEnv+"="+t.TempDir(),
		),
		Stderr: stderr,
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &isolationHelper{cmd: cmd, in: in, got: make(chan string, 1), waitCh: make(chan error, 1), stderr: stderr}
	go func() {
		line, rerr := bufio.NewReader(out).ReadString('\n')
		if rerr != nil {
			h.got <- "ERR:" + rerr.Error()
			return
		}
		h.got <- strings.TrimSpace(line)
	}()
	go func() { h.waitCh <- cmd.Wait() }()
	t.Cleanup(func() { h.stop(t) })
	return h
}

// stop kills a still-running helper and reaps it.
func (h *isolationHelper) stop(t *testing.T) {
	t.Helper()
	if h.exited {
		return
	}
	if kerr := h.cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
		t.Logf("kill helper: %v", kerr)
	}
	select {
	case werr := <-h.waitCh:
		t.Logf("helper exit: %v", werr)
	case <-time.After(10 * time.Second):
		t.Logf("helper did not exit after kill")
	}
}

// expectSwallowed asserts the reader child consumed the frame (control).
func (h *isolationHelper) expectSwallowed(t *testing.T) {
	t.Helper()
	select {
	case out := <-h.got:
		if out == "PING-FRAME" {
			t.Fatalf("negative control received %q; the frame was not swallowed", out)
		}
	case werr := <-h.waitCh:
		h.exited = true
		if werr != nil {
			t.Logf("helper exit (control): %v", werr)
		}
	case <-time.After(10 * time.Second):
	}
}

// expectFrame asserts the isolated helper echoed the protocol frame.
func (h *isolationHelper) expectFrame(t *testing.T) {
	t.Helper()
	var line string
	select {
	case out := <-h.got:
		if strings.HasPrefix(out, "ERR:") {
			t.Fatalf("helper stdout closed before the frame: %s\nstderr:\n%s", out, h.stderr.String())
		}
		line = out
	case werr := <-h.waitCh:
		h.exited = true
		t.Fatalf("helper exited before the frame: %v\nstderr:\n%s", werr, h.stderr.String())
	case <-time.After(90 * time.Second):
		t.Fatalf("protocol frame lost after 1.000 child spawns\nstderr:\n%s", h.stderr.String())
	}
	if line != "PING-FRAME" {
		t.Fatalf("frame = %q, want PING-FRAME", line)
	}
	// The helper exits after echoing; reap it so cleanup does not kill a
	// process that already finished.
	select {
	case werr := <-h.waitCh:
		h.exited = true
		if werr != nil {
			t.Fatalf("helper exit after frame: %v", werr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not exit after the frame")
	}
}

// runIsolationHelper is the child process: it isolates, spawns 1.000 children
// that inherited os.Stdin, then one reader child, and finally echoes the
// transport frame it can still read.
func runIsolationHelper(t *testing.T) {
	t.Helper()
	dataDir := os.Getenv(isolationDataEnv)
	privateIn, privateOut := os.Stdin, os.Stdout
	skip := os.Getenv(isolationSkipEnv) == "true"
	if !skip {
		privateIn, privateOut = helperIsolate(t, dataDir)
	}
	for range 1000 {
		if err := spawnNoop(t.Context()); err != nil {
			t.Fatalf("spawn: %v", err)
		}
	}
	// The reader exits non-zero when the inherited stdin is already EOF
	// (/dev/null); that is the successful isolated case, not a failure.
	rerr := spawnReader(t.Context())
	if skip && rerr != nil {
		t.Fatalf("negative-control reader did not consume the frame: %v", rerr)
	}
	if !skip && rerr != nil {
		t.Logf("reader child (expected EOF): %v", rerr)
	}
	line, err := bufio.NewReader(privateIn).ReadString('\n')
	if err != nil {
		t.Fatalf("read transport: %v", err)
	}
	if _, err := fmt.Fprint(privateOut, line); err != nil {
		t.Fatalf("write transport: %v", err)
	}
}

// helperIsolate isolates the helper's stdio and returns the transport pair.
func helperIsolate(t *testing.T, dataDir string) (in, out *os.File) {
	t.Helper()
	in, out, logf, err := isolateACPStdio(dataDir)
	if err != nil {
		t.Fatalf("isolate: %v", err)
	}
	if logf != nil {
		t.Cleanup(func() {
			if cerr := logf.Close(); cerr != nil {
				t.Logf("log close: %v", cerr)
			}
		})
	}
	return in, out
}

// spawnNoop starts a child that forwards os.Stdin but never reads it.
func spawnNoop(ctx context.Context) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", "exit 0")
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("noop child: %w", err)
	}
	return nil
}

// spawnReader starts a child that consumes one line from the inherited stdin.
func spawnReader(ctx context.Context) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", "set /p X=")
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", "IFS= read -r x")
	}
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("reader child: %w", err)
	}
	return nil
}
