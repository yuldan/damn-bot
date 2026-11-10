//go:build minimal

package cron

import (
	"context"
	"errors"
	"time"
)

// ErrDAGUnavailable is returned in the minimal build: dependencies are a
// serve-only feature, so a job with After fails closed instead of running in
// the wrong order.
var ErrDAGUnavailable = errors.New("cron: after dependencies unavailable in minimal build")

// ValidateDAG refuses any dependency in the minimal build.
func ValidateDAG(jobs []Job) error {
	for i := range jobs {
		if len(jobs[i].After) > 0 {
			return ErrDAGUnavailable
		}
	}
	return nil
}

func resolveAfter([]Job) {}

func (s *Scheduler) upstream(context.Context, *Job, time.Time) (string, bool, bool, error) {
	return "", true, false, nil
}
