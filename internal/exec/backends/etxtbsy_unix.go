//go:build unix

package backends

import (
	"errors"
	"syscall"
)

// isETXTBSY reports the transient "text file busy" spawn error. It is raised
// when the executable is still open for writing by a process forked during a
// write-then-rename fixture install; the command itself never ran.
func isETXTBSY(err error) bool { return errors.Is(err, syscall.ETXTBSY) }
