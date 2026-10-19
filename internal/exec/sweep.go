//go:build unix

package exec

import (
	"bytes"
	"errors"
	"os"
	"strconv"
)

// SweepStale kills every process carrying the marker of a damnbot process
// that no longer runs (crash, kill -9): the "sweep on the next start" of
// §A.4. The composition root calls it once at startup. Markers of live
// owners — another damnbot instance — are left alone; a recycled owner
// pid only makes the sweep skip (safe direction).
func SweepStale() (int, error) {
	self := os.Getpid()
	pids, err := markerPids(func(v []byte) bool {
		owner, ok := ownerPid(v)
		return ok && owner != self && !processAlive(owner)
	})
	killed := 0
	for _, pid := range pids {
		if kerr := killPid(pid); kerr != nil {
			err = errors.Join(err, kerr)
			continue
		}
		killed++
	}
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
