//go:build !unix && !windows

package ctxdb

import "runtime"

// crashSelf ends the helper goroutine on platforms without a kill signal.
func crashSelf() {
	runtime.Goexit()
}
