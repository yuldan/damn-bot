package main

import (
	"context"
	"time"
)

// Idle poll intervals shared by the outbox delivery loop and the two CLI
// spool loops: fast while a source yields work, backing off while it stays
// empty. The 1 s ceiling keeps queue and CLI latency ≤ 1 s (SCORECARD §1.2
// idle CPU).
const (
	pollFast = 200 * time.Millisecond
	pollMid  = 500 * time.Millisecond
	pollIdle = time.Second
)

// runPollLoop calls poll until ctx ends and uses its result (number of work
// items handled) to pace the next call: any work resets to pollFast, an empty
// poll steps 200 → 500 → 1000 ms.
func runPollLoop(ctx context.Context, poll func() int) {
	interval := pollFast
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		interval = nextPollInterval(interval, poll())
		timer.Reset(interval)
	}
}

// nextPollInterval implements the idle backoff table.
func nextPollInterval(cur time.Duration, worked int) time.Duration {
	if worked > 0 {
		return pollFast
	}
	if cur < pollMid {
		return pollMid
	}
	return pollIdle
}
