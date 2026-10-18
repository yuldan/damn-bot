//go:build linux || darwin

package ctxdb

import (
	"errors"
	"io/fs"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// Shared by the Linux and macOS probes; each OS file supplies procStat
// (start stamp + process group, zombies = fs.ErrNotExist) and pgroupMembers.

func clockMs(id int32) int64 {
	var ts unix.Timespec
	if unix.ClockGettime(id, &ts) != nil {
		return 0 // cannot fail for the clock ids used on Linux ≥ 2.6.39 / macOS ≥ 10.12
	}
	return ts.Nano() / 1e6
}

// Gone reports an owner on this host as provably absent (SPEC §11.11.1):
// PID missing, or both start stamps known and different. Unknown → present.
func (SystemProcs) Gone(id Ident) bool {
	st, _, err := procStat(id.PID)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return id.Start != "" && st != "" && st != id.Start
}

// killGroup SIGKILLs process group pgid if a member started at or after
// startStamp is alive (AF-07). Start stamps on both OSes are decimal
// integers that grow with process creation, so they compare numerically; an
// unparsable recorded stamp matches any live member (kill rather than leak).
// pgid ≤ 1 is refused: kill(0) targets our own group and kill(-1) every
// process we may signal.
func killGroup(pgid int, startStamp string) bool {
	if pgid <= 1 {
		return false
	}
	want, werr := strconv.ParseUint(startStamp, 10, 64)
	for _, st := range pgroupMembers(pgid) {
		if got, gerr := strconv.ParseUint(st, 10, 64); werr != nil || gerr == nil && got >= want {
			return syscall.Kill(-pgid, syscall.SIGKILL) == nil
		}
	}
	return false
}
