// Package wake is the wake-word seam of the voice module (SPEC §9.5, Q8):
// detection is an external command, the engine is disabled by default, and no
// microphone is opened in-process (ADR-001).
package wake

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one engine invocation.
	DefaultTimeout = 30 * time.Second
)

var (
	// ErrDisabled is a call on an engine that was not enabled.
	ErrDisabled = errors.New("voice/wake: engine is disabled")
	// ErrNotConfigured is an enabled engine without a command.
	ErrNotConfigured = errors.New("voice/wake: no engine command configured")
	// ErrBinaryMissing is a preset binary that is not installed.
	ErrBinaryMissing = errors.New("voice/wake: engine binary is not installed")
	// ErrUnknownEngine is an engine name outside the closed preset set.
	ErrUnknownEngine = errors.New("voice/wake: unknown engine")
	// ErrMissingAsset is a preset without its model, keywords or secret.
	ErrMissingAsset = errors.New("voice/wake: engine asset is missing")
	// ErrTimeout means no wake word in the engine window.
	ErrTimeout = errors.New("voice/wake: no wake word detected")
	// ErrCommand is a process that failed for another reason.
	ErrCommand = errors.New("voice/wake: engine command failed")
)

// Engine is one wake-word source; Wait blocks until detection, silence, or
// failure.
type Engine interface {
	Enabled() bool
	Wait(ctx context.Context) error
}

// Config is the external engine; Enabled must be opted in explicitly. Engine
// selects a reviewed preset; Command is the low-level argv used when Engine is
// empty. Binary/Model/Keywords override the preset defaults. AccessKey is read
// from the secret seam by the caller and is never persisted in config.
type Config struct {
	Enabled   bool
	Engine    string
	Command   []string
	Binary    string
	Model     string
	Keywords  []string
	AccessKey string
	Timeout   time.Duration
}

// External maps exit 0 to wake, 1 to silence, any other status to a broken
// engine.
type External struct {
	cfg Config
}

// New validates an optional external engine. A preset name must be installed
// and its assets present; nothing is downloaded and the microphone is opened
// only after this succeeds.
func New(cfg *Config) (*External, error) {
	if cfg.Engine != "" {
		args, err := presetCommand(cfg)
		if err != nil {
			return nil, err
		}
		cfg.Command = args
	}
	if cfg.Enabled && len(cfg.Command) == 0 {
		return nil, fmt.Errorf("%w: wake_command", ErrNotConfigured)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &External{cfg: *cfg}, nil
}

// presetCommand resolves one closed preset to an argv and verifies every asset
// the caller owns. Nothing is downloaded; a missing binary or asset is final.
func presetCommand(cfg *Config) ([]string, error) {
	p, ok := PresetFor(cfg.Engine)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEngine, cfg.Engine)
	}
	if err := presetAssets(cfg, &p); err != nil {
		return nil, err
	}
	bin := cfg.Binary
	if bin == "" {
		bin = p.Binary
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("%w: %s; %s", ErrBinaryMissing, bin, p.Install)
	}
	args := make([]string, 0, len(p.Args)+3)
	args = append(args, path)
	args = append(args, p.Args...)
	switch p.Name {
	case EngineOpenWakeWord:
		args = append(args, "--model", cfg.Model)
	case EngineSherpa:
		args = append(args, "--keywords", strings.Join(cfg.Keywords, ","))
	case EnginePorcupine:
		args = append(args, "--access-key", cfg.AccessKey)
	}
	return args, nil
}

// presetAssets refuses a preset without the model, keywords or secret it needs.
func presetAssets(cfg *Config, p *Preset) error {
	switch p.Name {
	case EngineOpenWakeWord:
		if strings.TrimSpace(cfg.Model) == "" {
			return fmt.Errorf("%w: %s needs wake_model", ErrMissingAsset, p.Name)
		}
	case EngineSherpa:
		if len(cfg.Keywords) == 0 {
			return fmt.Errorf("%w: %s needs wake_keywords", ErrMissingAsset, p.Name)
		}
	case EnginePorcupine:
		if cfg.AccessKey == "" {
			return fmt.Errorf("%w: %s needs an access key from %s", ErrMissingAsset, p.Name, p.KeyEnv)
		}
	}
	return nil
}

// EngineName is the configured engine, empty when the raw-argv form is used.
func (e *External) EngineName() string {
	if e == nil {
		return ""
	}
	return e.cfg.Engine
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
	cmd := &exec.Cmd{Path: e.cfg.Command[0], Args: e.cfg.Command, WaitDelay: time.Second}
	err := runCommand(cctx, cmd)
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
		return fmt.Errorf("%w: %s: %w", ErrCommand, strings.Join(e.displayArgs(), " "), err)
	}
}

// displayArgs never echoes the Porcupine access key.
func (e *External) displayArgs() []string {
	args := make([]string, len(e.cfg.Command))
	copy(args, e.cfg.Command)
	if e.cfg.Engine == EnginePorcupine {
		for i := range args {
			if i > 0 && args[i-1] == "--access-key" {
				args[i] = "<redacted>"
			}
		}
	}
	return args
}

func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("voice/wake: start: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		return werr
	case <-ctx.Done():
		kerr := cmd.Process.Kill()
		werr := <-done
		if kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			return fmt.Errorf("voice/wake: kill: %w", kerr)
		}
		if werr != nil && !errors.Is(werr, os.ErrProcessDone) {
			return fmt.Errorf("voice/wake: %w", werr)
		}
		return fmt.Errorf("voice/wake: %w", ctx.Err())
	}
}
