//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/tools"
)

// withExecuteCode refuses to link the tool in the minimal build.
func withExecuteCode(_ *config.Config, _ exec.Backend, _ *exec.EnvPolicy, list []tools.Tool) ([]tools.Tool, error) {
	return list, nil
}
