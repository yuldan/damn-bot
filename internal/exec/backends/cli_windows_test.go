//go:build windows && !minimal

package backends

import (
	"os/exec"
	"testing"
	"time"
)

// TestWindowsTreeHelpers keeps the Windows implementations reachable for
// deadcode before the docker backend wires them into main.
func TestWindowsTreeHelpers(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit 0")
	prepareTree(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	killTree(cmd, 100*time.Millisecond)
	_ = cmd.Wait()
	if sig := exitSignal(&exec.ExitError{}); sig != "" {
		t.Fatalf("signal = %q", sig)
	}
}
