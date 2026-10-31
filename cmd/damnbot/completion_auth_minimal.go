//go:build minimal

package main

// addAuthCompletion is a no-op: auth is not compiled into minimal.
func addAuthCompletion(map[string][]string) {}
