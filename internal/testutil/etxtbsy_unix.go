//go:build unix

package testutil

import (
	"errors"
	"syscall"
)

// isETXTBSY reports whether err is the kernel's "text file busy" refusal,
// including the *fs.PathError that os/exec wraps around it.
func isETXTBSY(err error) bool { return errors.Is(err, syscall.ETXTBSY) }
