//go:build unix

package backends

import (
	"errors"
	"os"
	osexec "os/exec"
	"syscall"
	"time"
)

// prepareTree puts the CLI in its own process group so a kill reaches every
// helper it spawned.
func prepareTree(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree sends TERM to the group, waits up to grace, then KILLs it.
func killTree(cmd *osexec.Cmd, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	//nolint:errcheck,gosec // ESRCH means the group is already gone.
	syscall.Kill(-pid, syscall.SIGTERM)
	if grace <= 0 {
		grace = time.Second
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) && processAlive(pid) {
		t := time.NewTimer(10 * time.Millisecond)
		<-t.C
	}
	//nolint:errcheck,gosec // ESRCH means the group is already gone.
	syscall.Kill(-pid, syscall.SIGKILL)
}

// exitSignal names the signal that killed the CLI.
func exitSignal(err *osexec.ExitError) string {
	ws, ok := err.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ""
	}
	return ws.Signal().String()
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

var _ = os.Getpid
