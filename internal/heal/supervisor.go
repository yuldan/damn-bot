package heal

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// State is a state of the supervisor machine (SELF-HEALING §1.4).
type State string

const (
	// Starting is the state before startup recovery and the first listener.
	Starting State = "starting"
	// Healthy accepts new work.
	Healthy State = "healthy"
	// Degraded keeps the process alive read-only (SELF-HEALING §4, disk full).
	Degraded State = "degraded"
	// SafeMode runs only the core surface, gateway notifications and doctor.
	SafeMode State = "safe_mode"
	// Exited is terminal and carries the process exit code (§1.2).
	Exited State = "exited"
)

// Action is a daemon activity the supervisor refuses or permits by state.
type Action string

const (
	// ModuleStart starts a lazy module; denied in safe mode with
	// module_unavailable{reason:"safe_mode"} (SELF-HEALING §1.4).
	ModuleStart Action = "module_start"
	// AutoTurn is a turn nobody typed: gateway worker, cron, kanban, auto.
	// It is off in safe mode and rejected in degraded read-only mode.
	AutoTurn Action = "auto_turn"
	// ToolExec runs a tool. In safe mode tools never run automatically
	// (approval is effectively ask, without an allowlist).
	ToolExec Action = "tool_exec"
	// OwnerCommand is the safe-mode surface: /status, /logs, /doctor, /resume.
	OwnerCommand Action = "owner_command"
)

// Refusals; callers match with errors.Is. A *Denial names the state and the
// human-readable reason.
var (
	// ErrTransition rejects an edge outside the machine; the others are refusals.
	ErrTransition = errors.New("heal: invalid state transition")
	ErrNotReady   = errors.New("heal: supervisor is starting")
	ErrExited     = errors.New("heal: supervisor exited")
	ErrSafeMode   = errors.New("heal: safe mode")
	ErrDegraded   = errors.New("heal: degraded read-only")
)

// Denial is a refused action carrying the readable reason for `/status`, the
// banner and the owner notice (SELF-HEALING §1.4, §7). Permit produces it.
type Denial struct {
	Action Action
	State  State
	Reason string
	cause  error
}

// Error implements error.
func (d *Denial) Error() string {
	return fmt.Sprintf("heal: %s refused in %s: %s", d.Action, d.State, d.Reason)
}

// Unwrap maps a refusal to its sentinel for errors.Is.
func (d *Denial) Unwrap() error { return d.cause }

// Snapshot is an immutable view of the supervisor, safe to serialize.
type Snapshot struct {
	State    State     `json:"state"`
	Since    time.Time `json:"since"`
	Reason   string    `json:"reason,omitempty"`
	ExitCode int       `json:"exit_code,omitempty"`
}

// Supervisor is the explicit machine of SELF-HEALING §1.4:
// starting → healthy → degraded → safe_mode → exited(code). The zero value is
// not usable.
type Supervisor struct {
	mu     sync.Mutex
	state  State
	since  time.Time
	reason string
	code   int
	now    func() time.Time
}

// NewSupervisor returns a supervisor in Starting. now may be nil (time.Now).
func NewSupervisor(now func() time.Time) *Supervisor {
	if now == nil {
		now = time.Now
	}
	return &Supervisor{state: Starting, since: now(), now: now}
}

// Snapshot copies the current state.
func (s *Supervisor) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{State: s.state, Since: s.since, Reason: s.reason, ExitCode: s.code}
}

// Start completes startup: starting → healthy (readiness, SPEC §17).
func (s *Supervisor) Start() error { return s.from(Starting, Healthy, "") }

// Degrade enters degraded read-only: healthy → degraded (SELF-HEALING §4).
func (s *Supervisor) Degrade(reason string) error { return s.from(Healthy, Degraded, reason) }

// Recover leaves degraded read-only: degraded → healthy.
func (s *Supervisor) Recover() error { return s.from(Degraded, Healthy, "") }

// EnterSafeMode trips the breaker: starting|healthy|degraded → safe_mode.
func (s *Supervisor) EnterSafeMode(reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case SafeMode:
		return fmt.Errorf("%w: already in %s", ErrTransition, SafeMode)
	case Exited:
		return fmt.Errorf("%w: %s → %s (exit %d)", ErrTransition, s.state, SafeMode, s.code)
	case Starting, Healthy, Degraded:
		s.state, s.since, s.reason = SafeMode, s.now(), reason
		return nil
	}
	return fmt.Errorf("%w: %s → %s", ErrTransition, s.state, SafeMode)
}

// Resume leaves safe mode: safe_mode → healthy (`damnbot service resume`).
func (s *Supervisor) Resume() error { return s.from(SafeMode, Healthy, "") }

// Exit records the terminal state with the process exit code (SELF-HEALING
// §1.2); the caller performs os.Exit.
func (s *Supervisor) Exit(code int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == Exited {
		return fmt.Errorf("%w: already exited (%d)", ErrTransition, s.code)
	}
	s.state, s.since, s.reason, s.code = Exited, s.now(), "", code
	return nil
}

// from applies one explicit edge; every other source state is a refusal.
func (s *Supervisor) from(want, to State, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != want {
		return fmt.Errorf("%w: %s → %s", ErrTransition, s.state, to)
	}
	s.state, s.since, s.reason = to, s.now(), reason
	return nil
}

// Permit reports whether the current state allows a. Safe mode keeps only the
// owner surface; degraded read-only rejects new turns (SELF-HEALING §1.4, §4).
func (s *Supervisor) Permit(a Action) error {
	snap := s.Snapshot()
	switch snap.State {
	case Healthy:
		return nil
	case Starting:
		return deny(snap, a, "not ready", ErrNotReady)
	case Degraded:
		if a == AutoTurn {
			return deny(snap, a, "new turns are rejected while read-only", ErrDegraded)
		}
		return nil
	case SafeMode:
		switch a {
		case OwnerCommand:
			return nil
		case ModuleStart:
			return deny(snap, a, `module_unavailable{reason:"safe_mode"}`, ErrSafeMode)
		case AutoTurn:
			return deny(snap, a, "automatic turns are off", ErrSafeMode)
		case ToolExec:
			return deny(snap, a, "tools never run automatically", ErrSafeMode)
		}
		return deny(snap, a, "unknown action", ErrSafeMode)
	case Exited:
		return deny(snap, a, fmt.Sprintf("process exited (code %d)", snap.ExitCode), ErrExited)
	}
	return deny(snap, a, "unknown state", ErrTransition)
}

// deny builds a refusal that also carries the state's own reason, so the
// operator reads why the daemon is in that mode.
func deny(snap Snapshot, a Action, why string, cause error) error {
	if snap.Reason != "" {
		why += ": " + snap.Reason
	}
	return &Denial{Action: a, State: snap.State, Reason: why, cause: cause}
}
