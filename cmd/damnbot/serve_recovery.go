package main

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/damnbot/damnbot/internal/update"
)

// earlyRecovery runs before the daemon opens the DB or takes the lease. An
// interrupted install whose pending target no longer matches the binary (or
// fails health) restores .prev; the database is never rolled back or deleted.
func earlyRecovery(ctx context.Context, dataDir string, stderr io.Writer) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	status, rerr := update.RepairLocal(ctx, &update.InstallOptions{
		DataDir: dataDir, BinaryPath: exe,
	})
	switch {
	case errors.Is(rerr, update.ErrNoPending):
		return
	case rerr != nil:
		sessionsPrintf(stderr, "serve: early recovery: %s\n", cleanLine(rerr.Error()))
		return
	}
	sessionsPrintf(stderr, "serve: early recovery: %s\n", cleanLine(status))
}
