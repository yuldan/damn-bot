//go:build !unix

package backends

// isETXTBSY: Windows has no text-file-busy spawn error.
func isETXTBSY(error) bool { return false }
