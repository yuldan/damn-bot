package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// MaxGateBytes bounds cron_jobs.gate (COST-13).
const MaxGateBytes = 2000

// ErrGateInvalid rejects a malformed gate document.
var ErrGateInvalid = errors.New("cron: invalid gate")

// Gate is the parsed cron_jobs.gate JSON: a wake verdict or a script.
type Gate struct {
	Wake    *bool    `json:"wake,omitempty"`
	Context string   `json:"context,omitempty"`
	Script  []string `json:"script,omitempty"`
	Timeout int      `json:"timeout_s,omitempty"`
}

// ParseGate decodes and validates one gate document. Empty means no gate.
func ParseGate(raw string) (*Gate, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > MaxGateBytes {
		return nil, fmt.Errorf("%w: %d bytes > %d", ErrGateInvalid, len(raw), MaxGateBytes)
	}
	var g Gate
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGateInvalid, err)
	}
	n := 0
	if g.Wake != nil {
		n++
	}
	if len(g.Script) > 0 {
		n++
	}
	if n != 1 {
		return nil, fmt.Errorf("%w: exactly one of wake or script is required", ErrGateInvalid)
	}
	return &g, nil
}

// GateResult is the verdict of one gate evaluation.
type GateResult struct {
	Wake    bool
	Reason  string
	Context string
}

// GateRunner evaluates Job.Gate before the executor runs (COST-13).
type GateRunner interface {
	Check(ctx context.Context, j *Job, g *Gate) (GateResult, error)
}

// Gates is the built-in gate runner. A nil Runner is a configuration error.
type Gates struct {
	Runner  *exec.Runner
	Timeout time.Duration
}

// Check evaluates g for j.
func (g *Gates) Check(ctx context.Context, j *Job, gate *Gate) (GateResult, error) {
	switch {
	case gate.Wake != nil:
		return GateResult{Wake: *gate.Wake, Reason: "verdict", Context: gate.Context}, nil
	case len(gate.Script) > 0:
		return g.script(ctx, j, gate)
	}
	return GateResult{}, fmt.Errorf("%w: empty", ErrGateInvalid)
}

func (g *Gates) script(ctx context.Context, j *Job, gate *Gate) (GateResult, error) {
	if g == nil || g.Runner == nil {
		return GateResult{}, errors.New("cron: gate script runner is not wired")
	}
	spec, _, err := exec.Prepare(&exec.OneShot{
		Direct: gate.Script, Dir: j.Cwd, Root: j.Cwd,
		Timeout: g.timeout(gate), Parent: os.Environ(), Policy: &exec.EnvPolicy{},
	})
	if err != nil {
		return GateResult{}, fmt.Errorf("cron: prepare gate: %w", err)
	}
	res, err := g.Runner.Run(ctx, &spec)
	if err != nil {
		return GateResult{}, fmt.Errorf("cron: run gate: %w", err)
	}
	if res.Status != exec.StatusExited || res.ExitCode == nil {
		return GateResult{}, fmt.Errorf("cron: gate status %s", res.Status)
	}
	switch *res.ExitCode {
	case 0:
		return GateResult{Wake: true, Reason: "script exit 0", Context: gate.Context}, nil
	case 1:
		return GateResult{Wake: false, Reason: "script exit 1"}, nil
	default:
		return GateResult{}, fmt.Errorf("cron: gate exit %d", *res.ExitCode)
	}
}

func (g *Gates) timeout(gate *Gate) time.Duration {
	if gate.Timeout > 0 {
		return min(time.Duration(gate.Timeout)*time.Second, exec.MaxTimeout)
	}
	if g.Timeout > 0 {
		return min(g.Timeout, exec.MaxTimeout)
	}
	return 30 * time.Second
}
