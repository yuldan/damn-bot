//go:build !race

package timing

// RaceEnabled reports a -race build (paired with race_on.go, NEW-35).
const RaceEnabled = false
