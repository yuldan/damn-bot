//go:build unix && !linux

package sandbox

import (
	"io/fs"
	"os"
)

// openBeneath on macOS/BSD: the openat(O_NOFOLLOW) walk is the anti-TOCTOU
// mechanism, followed by the device/inode re-check of verifyInode.
// fcntl(F_GETPATH) is not used: x/sys exposes no F_GETPATH wrapper and
// unsafe is banned (E-38, ADR-097).
func (*Sandbox) openBeneath(dir, rel, want string, flag int, perm fs.FileMode) (*os.File, error) {
	f, err := walkOpen(dir, rel, want, flag, perm)
	if err != nil {
		return nil, err
	}
	return verifyInode(f, want)
}

// handlePath: macOS/BSD cannot report an fd's path without F_GETPATH
// (ADR-097 butir 4); vet relies on the identity check there.
func handlePath(*os.File) (string, bool) { return "", false }
