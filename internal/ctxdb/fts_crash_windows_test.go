//go:build windows

package ctxdb

import "golang.org/x/sys/windows"

// crashSelf terminates the helper process abruptly, matching kill -9.
func crashSelf() {
	_ = windows.TerminateProcess(windows.CurrentProcess(), 9)
}
