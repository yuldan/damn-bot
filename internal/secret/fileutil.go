package secret

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

func syncRootDir(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return fmt.Errorf("secret: open config dir: %w", err)
	}
	serr := d.Sync()
	cerr := d.Close()
	if runtime.GOOS == "windows" {
		return cerr //nolint:wrapcheck // directory close on Windows
	}
	return errors.Join(serr, cerr)
}
