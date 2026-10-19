//go:build !linux && !darwin && !windows

package codetools

import "os"

// keyOf builds the cache/stat-tag identity of fi (CODE-TOOLS §5.1, §7).
// Without an inode in os.FileInfo (Windows) the path stands in for it.
func keyOf(path string, fi os.FileInfo) fileKey {
	return fileKey{path: path, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}
}
