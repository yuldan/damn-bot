//go:build windows

package testutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// replaceAttempts bounds the retry for Windows sharing violations.
const replaceAttempts = 3

// replaceFile installs tmp as dst. MoveFileEx refuses to replace an open
// destination, so it is removed first; only ERROR_ACCESS_DENIED is retried.
func replaceFile(tmp, dst string) error {
	var err error
	for attempt := range replaceAttempts {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
		}
		if err = os.Remove(dst); err == nil || errors.Is(err, fs.ErrNotExist) {
			if err = os.Rename(tmp, dst); err == nil {
				return nil
			}
			err = fmt.Errorf("rename %s: %w", tmp, err)
		} else {
			err = fmt.Errorf("remove %s: %w", dst, err)
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
	}
	return err
}
