package gateway

import (
	"context"
	"time"
)

// Runtime is the live serve state behind the implemented session and owner
// commands (/stop, /new, /status, /usage). A nil Runtime keeps all four
// registered but unsupported. No method starts a turn, calls a model, or runs
// a tool: StopTurn only cancels, NewSession only binds, and Status/Usage only
// read.
//
// The interface lives here, on the consumer side (E-09); the composition root
// implements it over the scheduler, the supervisor, and the stores it already
// owns.
type Runtime interface {
	// StopTurn cancels the active turn of scope's chat route and principal, if
	// any. cancelled is false when no turn was active, so a second /stop is a
	// no-op rather than a second cancellation.
	StopTurn(ctx context.Context, scope *Scope) (cancelled bool, err error)
	// NewSession binds a fresh session to scope's route and returns its id.
	// The previous session stays stored and readable.
	NewSession(ctx context.Context, scope *Scope) (string, error)
	// Status reports the read-only daemon state of /status.
	Status(ctx context.Context) (RuntimeStatus, error)
	// Usage reports the usage ledger of scope's current session and of the
	// current day. ok is false when the ledger has no row for that session, so
	// /usage can say "no usage yet" instead of printing a zero.
	Usage(ctx context.Context, scope *Scope) (UsageReport, bool, error)
}

// RuntimeStatus is the read-only daemon state /status answers with.
type RuntimeStatus struct {
	Uptime      time.Duration
	QueueDepth  int
	ActiveTurns int
	SafeMode    bool
	Version     string
}

// UsageReport is the ledger totals /usage answers with: tokens and micro-USD
// of the session and of today. CostKnown is false when a counted call had no
// known price, so the reply never presents an unknown cost as zero.
type UsageReport struct {
	SessionIn         int64
	SessionOut        int64
	SessionCostMicros int64
	SessionCostKnown  bool
	DayCostMicros     int64
	DayCostKnown      bool
}
