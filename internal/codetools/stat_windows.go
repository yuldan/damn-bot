//go:build windows

package codetools

import (
	"os"
	"syscall"
)

// keyOf builds the cache/stat-tag identity of fi (CODE-TOOLS §5.1, §7).
// Windows has no inode in os.FileInfo; the creation time is stable across
// writes and shared by every hard link to the object, so it keys edit
// locks (CT27-5). Creation-time collisions only over-serialize.
func keyOf(path string, fi os.FileInfo) fileKey {
	k := fileKey{path: path, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano(), ino: uint64(fi.Sys().(*syscall.Win32FileAttributeData).CreationTime.Nanoseconds())}
	return k
}
