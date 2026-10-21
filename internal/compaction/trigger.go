// Package compaction owns the staged P8 compaction contract (SPEC §4.7).
// This F2 slice implements only the trigger: the deterministic prune and the
// single summary call are added by the compaction implementation. Keeping
// the trigger here gives the loop/assembly boundary one typed vocabulary
// without importing the implementation.
package compaction

import (
	"fmt"
	"math"
)

// maxPercentInput bounds both operands so percent arithmetic cannot wrap.
const maxPercentInput = int64(math.MaxInt64) / 100

// Kind is the action the loop must take for the current request estimate.
type Kind uint8

// Trigger kinds.
const (
	None     Kind = iota // below compact_at
	Compact              // at least compact_at, below overflow_at
	Overflow             // at least overflow_at: context_overflow
)

// String returns the stable name (diagnostics and tests).
func (k Kind) String() string {
	switch k {
	case None:
		return "none"
	case Compact:
		return "compact"
	case Overflow:
		return "overflow"
	}
	return "none"
}

// Thresholds are percentages of the context window (SPEC §12 [context] and
// §4.7): compact_at defaults to 75, overflow_at to 90. A zero field selects
// the default; an invalid pair is rejected by New.
type Thresholds struct {
	CompactAt  int
	OverflowAt int
}

// Default thresholds.
const (
	DefaultCompactAt  = 75
	DefaultOverflowAt = 90
)

// Trigger is the typed result of one comparison. Percent is -1 when the
// input cannot be evaluated (negative estimate or non-positive window), in
// which case Kind is None: an unknown window never causes a spurious
// compaction and never hides a true overflow that the caller already knows.
type Trigger struct {
	Kind     Kind
	Percent  int64
	Estimate int64
	Window   int64
}

// Checker owns the threshold comparison. Zero value is not usable; build it
// with New or Default.
type Checker struct {
	compactAt, overflowAt int64
}

// Decider is the one-method seam at the assembly/loop boundary (E-09). The
// loop keeps its CompactAt/OverflowAt configuration; the composition root
// adapts it to this interface, so internal/compaction need not be imported
// by internal/loop (E-01).
type Decider interface {
	Decide(estimate, window int64) Trigger
}

// New validates thresholds and returns a checker. Zero values take the
// defaults; compact_at must be in (0, overflow_at), overflow_at in
// (compact_at, 100].
func New(t Thresholds) (*Checker, error) {
	c, o := t.CompactAt, t.OverflowAt
	if c == 0 {
		c = DefaultCompactAt
	}
	if o == 0 {
		o = DefaultOverflowAt
	}
	if c <= 0 || o <= c || o > 100 {
		return nil, fmt.Errorf("compaction: invalid thresholds compact_at=%d overflow_at=%d", t.CompactAt, t.OverflowAt)
	}
	return &Checker{compactAt: int64(c), overflowAt: int64(o)}, nil
}

// Default returns the SPEC thresholds (75/90).
func Default() *Checker {
	return &Checker{compactAt: DefaultCompactAt, overflowAt: DefaultOverflowAt}
}

// Decide compares estimate tokens with the context window. The comparison is
// integer-only and uses the same formula as the loop's guard, so 75% is a
// compaction and 90% is an overflow exactly at the boundary.
func (c *Checker) Decide(estimate, window int64) Trigger {
	t := Trigger{Percent: -1, Estimate: estimate, Window: window}
	if estimate < 0 || window <= 0 {
		return t
	}
	// Domain bounds keep estimate*100 and the percent inside int64. A larger
	// estimate is already an overflow; a larger window cannot be reached.
	if estimate > maxPercentInput {
		t.Kind, t.Percent = Overflow, 100
		return t
	}
	if window > maxPercentInput {
		window = maxPercentInput
		t.Window = window
	}
	t.Percent = estimate * 100 / window
	switch {
	case t.Percent >= c.overflowAt:
		t.Kind = Overflow
	case t.Percent >= c.compactAt:
		t.Kind = Compact
	}
	return t
}

var _ Decider = (*Checker)(nil)
