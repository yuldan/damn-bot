//go:build windows

package ctxdb

import (
	"errors"
	"io/fs"
	"os/exec"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestWindowsF1EdgeCases covers the EC-WIN entries the roadmap places at F1
// that need a native Windows probe. EC-WIN-009 is the only one this package
// owns; the exec/codetools/cmd rows live next to their mechanism.
func TestWindowsF1EdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if g.Item(t, "EC-WIN-009", "F1", "covered") {
		t.Run("EC-WIN-009", ecWin009)
	}
}

// EC-WIN-009: STILL_ACTIVE (259) is also a legal exit code. A process that
// exited with 259 must read as gone, while a running process stays present
// (the negative control, which a code-only check cannot distinguish).
func ecWin009(t *testing.T) {
	exited := exec.Command("cmd", "/c", "exit", "259") //nolint:gosec // G204: fixed argv
	if err := exited.Start(); err != nil {
		t.Fatalf("start 259 helper: %v", err)
	}
	defer func() { _ = exited.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for !(SystemProcs{}).Gone(Ident{PID: exited.Process.Pid}) {
		if time.Now().After(deadline) {
			t.Fatalf("a process that exited with 259 still reads as present (pid %d)", exited.Process.Pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	live := exec.Command("cmd", "/c", "ping", "-n", "30", "127.0.0.1") //nolint:gosec // G204: fixed argv
	if err := live.Start(); err != nil {
		t.Fatalf("start live helper: %v", err)
	}
	defer func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	}()
	if (SystemProcs{}).Gone(Ident{PID: live.Process.Pid}) {
		t.Fatalf("live pid %d reads as gone", live.Process.Pid)
	}
	if _, err := procStart(live.Process.Pid); err != nil {
		t.Fatalf("start stamp of a live pid: %v", err)
	}
	if _, err := procStart(exited.Process.Pid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("start stamp of an exited pid: %v, want fs.ErrNotExist", err)
	}
}
