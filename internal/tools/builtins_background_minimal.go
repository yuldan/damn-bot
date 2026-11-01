//go:build minimal

package tools

import (
	"context"
	"time"

	"github.com/damnbot/damnbot/internal/exec/classify"
)

// shellArgs is the minimal schema input (no background).
type shellArgs struct {
	Command  string `json:"command"`
	TimeoutS int    `json:"timeout_s"`
	Cwd      string `json:"cwd"`
	Session  string `json:"session"`
	Stdin    string `json:"stdin"`
}

func shellSchema() string {
	return `{"name":"shell","description":"Run one command in a process group with timeout. Asks for approval unless the exact argv matches shell_allow.","parameters":{"type":"object","properties":{"command":{"type":"string","minLength":1,"maxLength":65536},"timeout_s":{"type":"integer","minimum":1,"maximum":600},"cwd":{"type":"string"},"session":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},"stdin":{"type":"string","maxLength":65536}},"required":["command"],"additionalProperties":false}}`
}

func backgroundRequested(shellArgs) bool { return false }

func validateBackground(shellArgs) error { return nil }

func (t *shellTool) backgroundRun(context.Context, Env, shellArgs, string, time.Duration, classify.Verdict) (Result, bool, error) {
	return Result{}, false, nil
}
