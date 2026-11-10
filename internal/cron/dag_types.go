package cron

import (
	"context"
	"time"
)

// Upstream is the completed run of one dependency at the same slot.
type Upstream struct {
	ExecutionID string
	FinishedAt  time.Time
}

// UpstreamStore is the optional dependency lookup. A scheduler with a job
// that has After and a store without this method fails closed.
type UpstreamStore interface {
	UpstreamRun(ctx context.Context, jobID string, at time.Time) (Upstream, bool, error)
}
