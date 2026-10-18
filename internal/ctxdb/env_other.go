//go:build !linux

package ctxdb

import (
	"os"
	"time"
)

// Non-Linux fallbacks: the F0 spike targets Linux; macOS/Windows clock and
// process probes are F1 work (SPEC §11.11.1 [INFERENCE] items).

// BootMs falls back to the Go monotonic clock.
func (SystemClock) BootMs() int64 { return time.Now().UnixMilli() }

// MonoMs falls back to the wall clock reading's monotonic part.
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

func pgroupMembers(int) map[int]string { return nil }

func killGroup(int, string) bool { return false }
