//go:build !linux

package ctxdb

import (
	"os"
	"time"
)

// Non-Linux fallbacks: the F0 spike targets Linux; macOS/Windows clock and
// process probes are F1 work (SPEC §11.11.1 [INFERENCE] items).
var procEpoch = time.Now()

// BootMs falls back to the Go monotonic clock.
func (SystemClock) BootMs() int64 { return time.Since(procEpoch).Milliseconds() }

// MonoMs falls back to the Go monotonic clock.
func (SystemClock) MonoMs() int64 { return time.Since(procEpoch).Milliseconds() }

// SelfIdent returns a partial identity (start/boot unknown → '').
func SelfIdent() Ident {
	host, _ := os.Hostname()
	return Ident{Host: host + "#", PID: os.Getpid()}
}

// Gone never proves absence without an OS probe (unknown → present).
func (SystemProcs) Gone(Ident) bool { return false }

func pgroupMembers(int) []int { return nil }

func killGroup(int, string) bool { return false }
