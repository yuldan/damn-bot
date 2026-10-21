//go:build !windows

package secret

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func checkFile(fi fs.FileInfo) error {
	if err := checkRegular(fi); err != nil {
		return err
	}
	return checkOwner(fi)
}

func checkDir(fi fs.FileInfo) error {
	if !fi.IsDir() {
		return fmt.Errorf("secret: key path is not a directory: %w", ErrKeyFile)
	}
	return checkOwner(fi)
}

func checkOwner(fi fs.FileInfo) error {
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: mode %04o", ErrPermissions, fi.Mode().Perm())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: owner is not the current user", ErrPermissions)
	}
	return nil
}

func openNoFollow(root *os.Root, name string) (*os.File, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("secret: lstat %s: %w", name, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: symlink", ErrKeyFile)
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: symlink", ErrKeyFile)
		}
		return nil, fmt.Errorf("secret: open %s: %w", name, err)
	}
	return f, nil
}
