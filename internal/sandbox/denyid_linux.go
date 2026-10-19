//go:build linux

package sandbox

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// sameDenyID reports whether the opened object is one a fully literal deny
// pattern named at New (S16-2). Linux compares the tuple already taken by
// fstat, so the per-file check costs no extra syscall or allocation.
func (s *Sandbox) sameDenyID(st *unix.Stat_t, _ *os.File) (bool, error) {
	for _, id := range s.denyIDs {
		if s2, ok := id.Sys().(*syscall.Stat_t); ok && s2.Dev == st.Dev && s2.Ino == st.Ino {
			return true, nil
		}
	}
	return false, nil
}
