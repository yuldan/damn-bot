//go:build unix && !darwin

package exec

// groupLive reports whether process group pgid still has a live member. Only
// consulted for EPERM, which outside macOS means the member exists but may
// not be signalled (the group is not empty).
func groupLive(int) bool { return true }
