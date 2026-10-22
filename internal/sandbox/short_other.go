//go:build !windows

package sandbox

// shortFinal: 8.3 short names exist only on Windows.
func shortFinal(string) (string, error) { return "", nil }
