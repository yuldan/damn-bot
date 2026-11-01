package main

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
)

// newExecBackend selects the shell backend of this run from config
// (EXEC-AND-CODING §A.7, ADR-277). Only the core local backend exists in
// this slice; a remote id fails closed instead of falling back to local.
func newExecBackend(cfg *config.Config, runner *exec.Runner, sessDir, workspace, configDir string, env []string) (exec.Backend, *exec.Sessions, error) {
	switch cfg.Exec.Backend {
	case "local", "":
		return exec.NewLocal(runner), runner.NewSessions(sessDir, env), nil
	default:
		return nil, nil, fmt.Errorf("%w: %s (workspace %s, config %s): not built yet", exec.ErrUnavailable, cfg.Exec.Backend, workspace, configDir)
	}
}
