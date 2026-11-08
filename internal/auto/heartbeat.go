//go:build !minimal

package auto

import (
	"context"
	"fmt"
	"time"
)

// MinHeartbeatInterval is the parity floor for /heartbeat (AUTO-016).
const MinHeartbeatInterval = 60 * time.Second

// HeartbeatState is the opt-in idle-prompt state machine. It is owned by one
// session goroutine.
type HeartbeatState struct {
	interval time.Duration
	prompt   string
	paused   bool
}

// NewHeartbeat validates the interval and prompt.
func NewHeartbeat(interval time.Duration, prompt string) (*HeartbeatState, error) {
	if interval < MinHeartbeatInterval {
		return nil, fmt.Errorf("%w: heartbeat interval must be >= %s", ErrConfig, MinHeartbeatInterval)
	}
	if prompt == "" {
		return nil, fmt.Errorf("%w: heartbeat prompt is empty", ErrConfig)
	}
	return &HeartbeatState{interval: interval, prompt: prompt}, nil
}

// Pause stops future idle ticks.
func (h *HeartbeatState) Pause() { h.paused = true }

// Resume re-enables idle ticks after Pause.
func (h *HeartbeatState) Resume() { h.paused = false }

// Tick runs the heartbeat callback when the feature is enabled, the session
// is idle, and the beat is not paused. A disabled feature never runs it.
func (h *HeartbeatState) Tick(ctx context.Context, s *Service, idle bool, run func(context.Context, string) error) (bool, error) {
	if !s.Enabled(Heartbeat) {
		return false, fmt.Errorf("%w: %s", ErrDisabled, Heartbeat)
	}
	if h.paused || !idle {
		return false, nil
	}
	if run == nil {
		return false, fmt.Errorf("%w: heartbeat task is nil", ErrConfig)
	}
	if err := run(ctx, h.prompt); err != nil {
		return true, fmt.Errorf("auto: %s: %w", Heartbeat, err)
	}
	return true, nil
}

// Interval exposes the configured beat for the scheduler.
func (h *HeartbeatState) Interval() time.Duration { return h.interval }
