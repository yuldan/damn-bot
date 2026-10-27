//go:build unix

package exec

import (
	"bytes"
	"os"
	"strconv"
)

// SweepStale kills every process carrying the marker of a damnbot process
// that no longer runs (crash, kill -9): the "sweep on the next start" of
// §A.4. The composition root calls it once at startup. Markers of live
// owners — another damnbot instance — are left alone; a recycled owner
// pid only makes the sweep skip (safe direction). It returns only after the
// quiet observation of killMarked, so the killed processes are gone from the
// scan when it returns (the startup cost is sweepQuiet).
func SweepStale() (int, error) {
	self := os.Getpid()
	_, killed, err := killMarked(func(v []byte) bool {
		owner, ok := ownerPid(v)
		return ok && owner != self && !processAlive(owner)
	}, true)
	return killed, err
}

// ownerPid parses the owner of an exec id "<pid>.<random>".
func ownerPid(id []byte) (int, bool) {
	head, _, ok := bytes.Cut(id, []byte{'.'})
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(string(head))
	return pid, err == nil && pid > 0
}
