//go:build !minimal

package main

import (
	"fmt"
	"os"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/code"
	"github.com/damnbot/damnbot/internal/tools"
)

// executeCodeLinked reports whether execute_code is wired (full build).
const executeCodeLinked = true

// withExecuteCode appends the execute_code tool (ADR-089/296). The tool is
// !minimal so the minimal binary keeps its size budget.
func withExecuteCode(cfg *config.Config, backend exec.Backend, policy *exec.EnvPolicy, list []tools.Tool) ([]tools.Tool, error) {
	runner, err := code.New(code.Config{Backend: backend, Env: policy.Env(os.Environ(), true, "", "", "")})
	if err != nil {
		return nil, fmt.Errorf("execute_code: %w", err)
	}
	return append(list, tools.NewExecuteCode(&tools.ExecuteCodeConfig{Runner: runner, AutoLanguages: cfg.Exec.CodeAutoLanguages})), nil
}
