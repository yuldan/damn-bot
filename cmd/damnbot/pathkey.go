package main

import (
	"path/filepath"
)

// canonicalBase is the OS-neutral part of the payment-gate key: absolute,
// symlinks resolved (no /var vs /private/var or link vs target difference),
// cleaned and slash-separated. Each OS wrapper adds long-name expansion
// and/or case folding (SEC13 path canonicalization).
func canonicalBase(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		abs = r
	}
	return filepath.ToSlash(filepath.Clean(abs))
}
