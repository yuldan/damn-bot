//go:build unix

package update

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(f *os.File) (func() error, error) {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("update: lock: %w", err)
	}
	return func() error {
		if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
			return fmt.Errorf("update: unlock: %w", err)
		}
		return nil
	}, nil
}
