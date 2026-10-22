//go:build !windows && !darwin

package main

// monetaryKey is case-sensitive on Linux and other POSIX filesystems.
func monetaryKey(p string) string { return canonicalBase(p) }
