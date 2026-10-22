//go:build !windows

package sandbox

// canonicalFinal is an identity: EvalSymlinks in resolve already returned
// the canonical form on this OS.
func canonicalFinal(p string) (string, error) { return p, nil }
