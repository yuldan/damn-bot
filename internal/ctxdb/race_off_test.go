//go:build !race

package ctxdb

// raceEnabled: built without -race (see timingEnforced).
const raceEnabled = false
