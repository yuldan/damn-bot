//go:build perf

package timing

// The perf job runs on shared CI runners: absolute budgets get ×1.5 (ADR-077).
const (
	scaleNum = 3
	scaleDen = 2
)
