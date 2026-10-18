//go:build !linux

package ctxdb

// openUnder has no descriptor probe off Linux; on Windows the TempDir
// cleanup itself fails on a leaked handle.
func openUnder(string) []string { return nil }
