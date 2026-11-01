package exec

import (
	"context"
	"errors"
	"fmt"
)

// Backend runs command trees in a target environment (EXEC-AND-CODING §A.7).
// Implementations are lazy: connections and daemons open on the first Run,
// never at construction. A backend without a capability returns
// ErrCapability; it never falls back to local silently.
type Backend interface {
	ID() string
	Caps() Caps
	// Run executes one command; the contract is Runner.Run's (status set,
	// process tree killed as a whole, CancelDeadline).
	Run(ctx context.Context, spec *Spec) (Result, error)
	// Sweep kills the processes still carrying execID (0 orphan after a
	// crashed or timed-out call) and reports how many it killed.
	Sweep(ctx context.Context, execID string) (int, error)
	// Close kills every process the backend still owns.
	Close(ctx context.Context) error
}

// Caps declares what a backend enforces (EXEC-AND-CODING §A.7). A missing
// capability is reported as module_unavailable, never emulated silently.
type Caps struct {
	PTY            bool // PTY runner for interactive commands
	Signals        bool // SIGTERM → grace → SIGKILL on the command tree
	SharesFS       bool // the backend sees the workspace at identical paths
	ResourceLimits bool // CPU/memory/PIDs limits are enforceable
	Network        bool // network isolation (none or allowlist) is enforceable
	Sessions       bool // persistent shell sessions (EXEC-AND-CODING §A.2)
}

// Limits are the per-call resource caps a backend applies (zero: unset).
// A backend with Caps.ResourceLimits=false rejects a Spec that sets one.
type Limits struct {
	CPUMilli  int // 1000 = one CPU
	MemoryMiB int
	PIDs      int
}

// Set reports whether any limit is configured.
func (l Limits) Set() bool { return l.CPUMilli > 0 || l.MemoryMiB > 0 || l.PIDs > 0 }

// Backend errors.
var (
	// ErrUnavailable: the backend cannot run here (missing binary, daemon,
	// credentials or isolation support). Fail closed: no local fallback.
	ErrUnavailable = errors.New("exec: backend unavailable")
	// ErrCapability: the backend does not provide a requested capability.
	ErrCapability = errors.New("exec: backend capability missing")
)

// Require returns ErrCapability naming every capability the backend lacks.
func Require(b Backend, need Caps) error {
	got := b.Caps()
	var missing []string
	if need.PTY && !got.PTY {
		missing = append(missing, "pty")
	}
	if need.Signals && !got.Signals {
		missing = append(missing, "signals")
	}
	if need.SharesFS && !got.SharesFS {
		missing = append(missing, "shares_fs")
	}
	if need.ResourceLimits && !got.ResourceLimits {
		missing = append(missing, "resource_limits")
	}
	if need.Network && !got.Network {
		missing = append(missing, "network")
	}
	if need.Sessions && !got.Sessions {
		missing = append(missing, "sessions")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s: %v", ErrCapability, b.ID(), missing)
	}
	return nil
}

// NewExecID returns a fresh execution id "<pid>.<random>"; local SweepStale
// parses the owner pid, remote backends use it as their cleanup marker.
func NewExecID() string { return newExecID() }
