//go:build windows

package secret

import (
	"fmt"
	"io/fs"
	"os"
)

func checkFile(fi fs.FileInfo) error { return checkRegular(fi) }
func checkDir(fi fs.FileInfo) error {
	if !fi.IsDir() {
		return fmt.Errorf("secret: key path is not a directory: %w", ErrKeyFile)
	}
	return nil
}

func openNoFollow(root *os.Root, name string) (*os.File, error) {
	li, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("secret: lstat %s: %w", name, err)
	}
	if li.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("secret: key path is a symlink: %w", ErrKeyFile)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("secret: open %s: %w", name, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("secret: stat %s: %w", name, err)
	}
	if err := checkRegular(fi); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
