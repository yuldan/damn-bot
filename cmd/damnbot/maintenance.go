package main

import (
	"io"

	configos "github.com/damnbot/damnbot/internal/config"

	"github.com/damnbot/damnbot/internal/exec"
)

// maintenanceBlocked refuses destructive maintenance when the process is a
// child of a damnbot shell tool (the runner tags it with DAMNBOT_EXEC_ID).
// This is defense in depth only: unsetting the variable bypasses it, so the
// shell classifier remains the primary defense.
func maintenanceBlocked(w io.Writer, what string) bool {
	if configos.OS().Getenv(exec.MarkerEnv) == "" {
		return false
	}
	sessionsPrintf(w, "%s: refusing destructive maintenance from inside a damnbot shell tool (%s set); run it in your own shell\n",
		what, exec.MarkerEnv)
	return true
}
