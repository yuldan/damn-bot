package acp

import (
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/loop"
)

// Mode is a session approval mode (SPEC §13.3, §14.1). Plan is the plan-mode
// gate of SPEC §18.3; the ACP server never turns it into an approval prompt.
type Mode uint8

// Session modes.
const (
	ModeAsk Mode = iota
	ModeAutoEdit
	ModeAuto
	ModePlan
)

func (m Mode) String() string {
	switch m {
	case ModeAsk:
		return "ask"
	case ModeAutoEdit:
		return "auto-edit"
	case ModeAuto:
		return "auto"
	case ModePlan:
		return "plan"
	}
	return "unknown"
}

// Plan reports whether m is the plan-mode restriction.
func (m Mode) Plan() bool { return m == ModePlan }

// ParseMode validates the wire value of session/set_mode and session/new.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "ask":
		return ModeAsk, nil
	case "auto-edit":
		return ModeAutoEdit, nil
	case "auto":
		return ModeAuto, nil
	case "plan":
		return ModePlan, nil
	}
	return ModeAsk, fmt.Errorf("%w: %q", ErrMode, s)
}

// ErrMode reports an unknown session mode.
var ErrMode = errors.New("acp: unknown session mode")

// SetMode changes the session mode; a closed session refuses.
func (s *Session) SetMode(m Mode) error {
	if m > ModePlan {
		return ErrMode
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateClosed {
		return ErrSessionClosed
	}
	s.mode = m
	return nil
}

// Mode returns the current session mode.
func (s *Session) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// PlanMode reports whether the session is in plan mode.
func (s *Session) PlanMode() bool { return s.Mode().Plan() }

// PromptInput is the loop input for the next prompt; plan mode travels with
// it so the dispatcher gate (ADR-198) is the single enforcement point.
func (s *Session) PromptInput(text string) loop.Input {
	return loop.Input{Text: text, PlanMode: s.PlanMode()}
}
