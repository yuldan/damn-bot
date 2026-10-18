package exec

import (
	"errors"

	"golang.org/x/sys/windows"
)

// killPid: Windows trees die with their job; there is no marker sweep.
func killPid(int) error { return nil }

// isBrokenPipe: the child stopped reading its stdin.
func isBrokenPipe(err error) bool {
	return errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_NO_DATA)
}
