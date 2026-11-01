//go:build minimal

package main

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
)

// newRemoteExecBackend refuses every remote backend in the minimal build
// instead of linking the adapters (P13 minimal ≤ 22.000.000 B).
func newRemoteExecBackend(cfg *config.Config, workspace, configDir string, _ []string) (exec.Backend, *exec.Sessions, error) {
	return nil, nil, fmt.Errorf("%w: %s (workspace %s, config %s): minimal build", exec.ErrUnavailable, cfg.Exec.Backend, workspace, configDir)
}
