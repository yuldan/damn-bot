//go:build minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/tools"
)

// codeIntelToolsLinked reports whether repo_map/code_intel are wired (absent in minimal).
const codeIntelToolsLinked = false

// withCodeIntelTools is absent in the minimal build.
func withCodeIntelTools(_ []string, _ string, _ bool, list []tools.Tool) []tools.Tool {
	return list
}

// executeCodeLinked reports whether execute_code is wired (absent in minimal).
const executeCodeLinked = false

// withExecuteCode refuses to link the tool in the minimal build.
func withExecuteCode(_ *config.Config, _ exec.Backend, _ *exec.EnvPolicy, list []tools.Tool) ([]tools.Tool, error) {
	return list, nil
}

// processToolsLinked reports whether the process_* adapters are wired.
const processToolsLinked = false

// newProcessSupport is absent in the minimal build.
func newProcessSupport() (processSupport, error) { return processSupport{}, nil }

// processSupport is empty in the minimal build.
type processSupport struct{}

func (processSupport) toolList() []tools.Tool               { return nil }
func (processSupport) closeFn() func(context.Context) error { return nil }
func (processSupport) attachFn() func(*tools.ShellConfig)   { return nil }
