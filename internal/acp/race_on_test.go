//go:build race

package acp

// raceEnabled reports whether the race detector is active. The detector adds
// enough overhead that wall-clock budgets must account for it.
const raceEnabled = true
