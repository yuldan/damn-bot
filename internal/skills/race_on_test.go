//go:build race

package skills

// raceEnabled skips wall-clock assertions under the race detector, where the
// instrumentation dominates the measurement (SPEC §16).
const raceEnabled = true
