//go:build windows

package backends

import (
	osexec "os/exec"
	"time"
)

// prepareTree keeps the default attributes: the Job Object of the CLI is not
// available to a child-less process here, so Close/Sweep remove containers
// explicitly.
func prepareTree(*osexec.Cmd) {}

// killTree kills the CLI; a container is removed by the backend cleanup.
func killTree(cmd *osexec.Cmd, _ time.Duration) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// exitSignal reports no POSIX signal name on Windows.
func exitSignal(*osexec.ExitError) string { return "" }
