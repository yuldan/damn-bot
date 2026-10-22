//go:build perf

package timing

// The perf job runs on shared CI runners: absolute budgets get ×1.5 (ADR-077),
// and fsync-bound budgets are taken relative to a durable-write reference
// measured on the same runner (ADR-127).
const (
	scaleNum     = 3
	scaleDen     = 2
	SharedRunner = true
)
