//go:build windows

package stt

import "testing"

// makeExec is a no-op: POSIX shell fixtures skip on Windows.
func makeExec(*testing.T, string) {}
