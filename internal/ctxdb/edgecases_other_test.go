//go:build !unix

package ctxdb

import "github.com/damnbot/damnbot/internal/testutil/phase"

// platformStateCases: EC-STATE-016 (a forked child inheriting a POSIX flock)
// has no counterpart on this OS; tryLock itself stays fail-closed.
func platformStateCases(phase.Gate) []stateCase { return nil }
