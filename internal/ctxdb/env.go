package ctxdb

import (
	"context"
	"time"
)

// Clock is the only time source of ctxdb (SPEC §11.11.1). Liveness, TTL and
// heartbeat use BootMs; the 3 s two-observation rule uses MonoMs, which does
// not advance while the machine is suspended; WallMs is for created_at only.
type Clock interface {
	BootMs() int64
	MonoMs() int64
	WallMs() int64
	// Sleep waits d or until ctx is done (duty-cycle pauses of gc/purge).
	Sleep(ctx context.Context, d time.Duration) error
}

// Ident is a process identity (SPEC §11.11.1): host = "<hostname>#<pidns>",
// Start = opaque OS start stamp compared verbatim, BootID opaque ('' unknown).
type Ident struct {
	Host   string `json:"owner_host"`
	PID    int    `json:"owner_pid"`
	Start  string `json:"owner_start"`
	BootID string `json:"boot_id"`
}

// Procs answers "is this owner provably gone?" for owners on this host.
type Procs interface {
	Gone(id Ident) bool
}

// SystemClock reads the kernel clocks.
type SystemClock struct{}

// WallMs returns wall-clock UTC milliseconds.
func (SystemClock) WallMs() int64 { return time.Now().UnixMilli() }

// Sleep waits without time.Sleep so cancellation is honoured (E-30).
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SystemProcs probes /proc (Linux) for owner existence.
type SystemProcs struct{}
