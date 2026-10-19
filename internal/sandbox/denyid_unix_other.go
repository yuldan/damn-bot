//go:build unix && !linux

package sandbox

import (
	"os"

	"golang.org/x/sys/unix"
)

// sameDenyID reports whether the opened object is one a fully literal deny
// pattern named at New (S16-2). Without a shared stat type (darwin, BSD)
// the handle's FileInfo is compared with os.SameFile.
func (s *Sandbox) sameDenyID(_ *unix.Stat_t, f *os.File) (bool, error) {
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	for _, id := range s.denyIDs {
		if os.SameFile(fi, id) {
			return true, nil
		}
	}
	return false, nil
}
