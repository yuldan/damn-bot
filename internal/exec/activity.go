package exec

import "time"

// Idle detection (§A.4): a tree is idle while it prints nothing, uses less
// than idleCPUShare of one core and moves less than idleIOBytes of I/O
// over the idle window.
const (
	idleSampleEvery = time.Second
	idleIOBytes     = 64 << 10
	idleCPUShare    = 100 // CPU < elapsed / 100 (1 % of one core)
)

// idleTracker accumulates tree activity since the last sign of life.
type idleTracker struct {
	since time.Time // start of the current idle window
	cpu   time.Duration
	io    uint64
}

// observe adds one sample (deltas since the previous one) and reports
// whether the tree has been idle for window. Output resets the window.
func (t *idleTracker) observe(now, lastOutput time.Time, cpu time.Duration, io uint64, measurable bool, window time.Duration) bool {
	if lastOutput.After(t.since) {
		t.since, t.cpu, t.io = lastOutput, 0, 0
	}
	if !measurable {
		return false // no CPU/I/O accounting: never idle-kill a possibly busy tree
	}
	t.cpu += cpu
	t.io += io
	if t.io >= idleIOBytes || t.cpu*idleCPUShare >= now.Sub(t.since) && t.cpu > 0 {
		t.since, t.cpu, t.io = now, 0, 0
		return false
	}
	return now.Sub(t.since) >= window
}
