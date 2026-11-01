package main

import (
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
)

// newExecBackend selects the shell backend of this run from config
// (EXEC-AND-CODING §A.7, ADR-275). Local is always available; remote
// backends live in exec_backend_remote.go behind !minimal so the minimal
// build keeps its size budget, and fail closed otherwise.
func newExecBackend(cfg *config.Config, runner *exec.Runner, sessDir, workspace, configDir string, env []string) (exec.Backend, *exec.Sessions, error) {
	if cfg.Exec.Backend == "local" || cfg.Exec.Backend == "" {
		return exec.NewLocal(runner), runner.NewSessions(sessDir, env), nil
	}
	return newRemoteExecBackend(cfg, workspace, configDir, env)
}
