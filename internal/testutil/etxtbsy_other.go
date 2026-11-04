//go:build !unix

package testutil

// isETXTBSY is always false off unix: those kernels have no ETXTBSY errno.
func isETXTBSY(error) bool { return false }
