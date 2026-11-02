package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// Local is the core backend: the process-tree runner of §A.1–A.4 on the
// host. It adds the Backend contract to Runner without changing Run
// semantics; the composition root keeps using the embedded Runner for
// persistent sessions.
type Local struct{ *Runner }

// Compile-time proof: the local backend satisfies the backend contract.
var _ Backend = (*Local)(nil)

// NewLocal returns the local backend over r.
func NewLocal(r *Runner) *Local { return &Local{Runner: r} }

// ID is the backend id stored in config and journaled with every call.
func (l *Local) ID() string { return "local" }

// Caps is the full local capability set: PTY, signals, shared filesystem and
// persistent sessions. Resource limits and network isolation are not
// enforced on the host (§14.2: isolation = none).
func (l *Local) Caps() Caps {
	return Caps{PTY: true, Signals: true, SharesFS: true, Sessions: true}
}

// Run rejects resource limits the host runner cannot enforce, instead of
// pretending to apply them (GAP-SEC-23 fail closed).
func (l *Local) Run(ctx context.Context, spec *Spec) (Result, error) {
	if spec.Limits.Set() {
		return Result{Status: StatusSpawnFailed}, fmt.Errorf("%w: local: resource_limits", ErrCapability)
	}
	res, err := l.Runner.Run(ctx, spec)
	res.Execution = Execution{Backend: "local"}
	return res, err
}

// Sweep kills every remaining process carrying execID's marker (§A.4).
func (l *Local) Sweep(ctx context.Context, execID string) (int, error) {
	return SweepMarker(ctx, execID)
}

// SweepMarker kills every process accepted by execID's marker and reports how
// many it killed. Local and the CLI container backends share it: the marker
// is inherited by every descendant, so a crashed owner is also covered by the
// startup sweep of §A.4.
func SweepMarker(ctx context.Context, execID string) (int, error) {
	if execID == "" {
		return 0, errors.Join(ErrSpec, errors.New("empty exec id"))
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("exec: sweep: %w", err)
	}
	_, killed, err := killMarked(func(v []byte) bool { return bytes.Equal(v, []byte(execID)) }, true)
	return killed, err
}

// Close kills the trees of this backend; it never abandons a runner task.
func (l *Local) Close(ctx context.Context) error { return l.CloseContext(ctx) }
