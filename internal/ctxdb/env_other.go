//go:build !linux && !darwin && !windows

package ctxdb

import (
	"os"
	"time"
)

// Fallbacks for OSes without a probe (only Linux, macOS and Windows have
// one): identity is partial and nothing is ever proven absent (unknown →
// present).

// BootMs falls back to the wall clock: the only origin shared across
// processes without an OS boot clock.
func (SystemClock) BootMs() int64 { return time.Now().UnixMilli() }

// MonoMs falls back to the wall clock as well.
func (SystemClock) MonoMs() int64 { return time.Now().UnixMilli() }

// SelfIdent returns a partial identity (start/boot unknown → ”).
func SelfIdent() Ident {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return Ident{Host: host + "#", PID: os.Getpid()}
}

// Gone never proves absence without an OS probe (unknown → present).
func (SystemProcs) Gone(Ident) bool { return false }

// killGroup is platform-n/a: no group is enumerated or signalled.
func killGroup(int, string) bool { return false }
