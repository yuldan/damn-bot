// Package wake is the wake-word seam of the voice module (SPEC §9.5, Q8):
// detection is an external command, the engine is disabled by default, and no
// microphone is opened in-process (ADR-001).
package wake

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one engine invocation.
	DefaultTimeout = 30 * time.Second
)

var (
	ErrDisabled      = errors.New("voice/wake: engine is disabled")
	ErrNotConfigured = errors.New("voice/wake: no engine command configured")
	ErrTimeout       = errors.New("voice/wake: no wake word detected")
	ErrCommand       = errors.New("voice/wake: engine command failed")
)

// Engine is one wake-word source; Wait blocks until detection, silence, or
// failure.
type Engine interface {
	Enabled() bool
	Wait(ctx context.Context) error
}

// Config is the external engine; Enabled must be opted in explicitly.
type Config struct {
	Enabled bool
	Command []string
	Timeout time.Duration
}

// External maps exit 0 to wake, 1 to silence, any other status to a broken
// engine.
type External struct {
	cfg Config
}

// New validates an optional external engine.
func New(cfg Config) (*External, error) {
	if cfg.Enabled && len(cfg.Command) == 0 {
		return nil, fmt.Errorf("%w: wake_command", ErrNotConfigured)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &External{cfg: cfg}, nil
}

// Enabled reports the explicit opt-in.
func (e *External) Enabled() bool { return e != nil && e.cfg.Enabled }

// Wait runs one bounded detection attempt.
func (e *External) Wait(ctx context.Context) error {
	if !e.Enabled() {
		return ErrDisabled
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("voice/wake: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, e.cfg.Command[0], e.cfg.Command[1:]...)
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return ErrTimeout
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%w after %s", ErrTimeout, e.cfg.Timeout)
	case cctx.Err() != nil:
		return fmt.Errorf("voice/wake: %w", cctx.Err())
	default:
		return fmt.Errorf("%w: %s: %w", ErrCommand, strings.Join(e.cfg.Command, " "), err)
	}
}
