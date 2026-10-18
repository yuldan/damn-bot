package codetools

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// setXattr labels path with a user xattr; file systems without user
// xattrs (ENOTSUP; EPERM on tmpfs) leave nothing to preserve.
func setXattr(t *testing.T, path string) {
	t.Helper()
	if err := unix.Setxattr(path, "user.damnbot", []byte("kept"), 0); err != nil && !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EPERM) {
		t.Fatalf("setxattr: %v", err)
	}
}
