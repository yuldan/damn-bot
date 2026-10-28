//go:build linux

package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestACPStdioFDIsolation (GAP-REL-13): the real binary serving ACP has fd 0
// on /dev/null and fd 1 on the ACP log, while a control process started with
// the same stdin pipe still shows that pipe — the measurement distinguishes
// a leaked transport descriptor from the isolated one.
func TestACPStdioFDIsolation(t *testing.T) {
	t.Parallel()
	e := New(t)
	c := e.acpStart(t)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	c.until(1)
	pid := c.cmd.Process.Pid
	if got := fdLink(t, pid, 0); got != os.DevNull {
		t.Fatalf("acp fd0 = %q, want %s", got, os.DevNull)
	}
	got1 := fdLink(t, pid, 1)
	if !strings.Contains(got1, "acp.log") {
		t.Fatalf("acp fd1 = %q, want the ACP log", got1)
	}
	if fi, err := os.Stat(got1); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("acp fd1 is not a log file: %q (%v)", got1, err)
	}

	// Negative control: the same standard input, no isolation.
	ctrl := exec.CommandContext(t.Context(), "sh", "-c", "sleep 2")
	in, err := ctrl.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if kerr := ctrl.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			t.Logf("kill control: %v", kerr)
		}
		if werr := ctrl.Wait(); werr != nil {
			t.Logf("control exit: %v", werr)
		}
		if cerr := in.Close(); cerr != nil {
			t.Logf("control stdin: %v", cerr)
		}
	})
	link := fdLink(t, ctrl.Process.Pid, 0)
	if !strings.HasPrefix(link, "pipe:") {
		t.Fatalf("control fd0 = %q, want pipe", link)
	}
}

// fdLink resolves /proc/<pid>/fd/<fd>, retrying while exec replaces the
// process image.
func fdLink(t *testing.T, pid, fd int) string {
	t.Helper()
	path := fmt.Sprintf("/proc/%d/fd/%d", pid, fd)
	deadline := time.Now().Add(3 * time.Second)
	for {
		link, err := os.Readlink(path)
		if err == nil && link != "" {
			return link
		}
		if time.Now().After(deadline) {
			t.Fatalf("readlink %s: %v", path, err)
		}
		<-time.After(20 * time.Millisecond)
	}
}
