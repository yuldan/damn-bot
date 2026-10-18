//go:build !linux

package sandbox

import "os"

// openResolved is openBeneath where openat2 does not exist.
func (s *Sandbox) openResolved(dir, rel, want string) (*os.File, error) {
	return s.openBeneath(dir, rel, want, os.O_RDONLY, 0)
}
