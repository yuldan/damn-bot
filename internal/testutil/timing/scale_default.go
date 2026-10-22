//go:build !perf

package timing

// Outside the perf build budgets apply as written, absolute (reference
// machine Q14 at acceptance, ADR-127).
const (
	scaleNum     = 1
	scaleDen     = 1
	SharedRunner = false
)
