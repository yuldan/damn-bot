//go:build minimal

package tools

import (
	"context"
	"time"
)

// backgroundLinked reports whether shell background is compiled in.
const backgroundLinked = false

// shellArgs is the minimal schema input (no background).
type shellArgs struct {
	Command  string `json:"command"`
	TimeoutS int    `json:"timeout_s"`
	Cwd      string `json:"cwd"`
	Session  string `json:"session"`
	Stdin    string `json:"stdin"`
}

func shellSchema() string {
	return `{"name":"shell","parameters":{"type":"object","properties":{"command":{"type":"string","minLength":1,"maxLength":65536},"timeout_s":{"type":"integer","minimum":1,"maximum":600},"cwd":{"type":"string"},"session":{"type":"string"},"stdin":{"type":"string","maxLength":65536}},"required":["command"],"additionalProperties":false}}`
}

func backgroundRequested(*shellArgs) bool { return false }

func validateBackground(*shellArgs) error { return nil }

func (t *shellTool) backgroundRun(context.Context, Env, *shellArgs, string, time.Duration) (Result, bool, error) {
	return Result{}, false, nil
}
