//go:build !windows

package main

// utf8Console is a no-op outside Windows: terminal encoding is the locale's
// business (EC-WIN-017 is Windows-only).
func utf8Console() {}
