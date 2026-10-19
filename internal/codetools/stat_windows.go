//go:build windows

package codetools

import (
	"os"

	"golang.org/x/sys/windows"
)

// keyOf builds the cache/stat-tag identity of fi (CODE-TOOLS §5.1, §7).
// Windows has no inode in os.FileInfo; the volume serial and file index
// from a handle identify the object across hard links and case variants
// (CT27-5). Without a handle the path stands in for it.
func keyOf(path string, fi os.FileInfo) fileKey {
	k := fileKey{path: path, size: fi.Size(), mtimeNs: fi.ModTime().UnixNano()}
	f, err := os.Open(path) //nolint:gosec // G304: caller-supplied workspace path.
	if err != nil {
		return k
	}
	defer f.Close() //nolint:errcheck // read-only identity probe.
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info) != nil {
		return k
	}
	k.dev = uint64(info.VolumeSerialNumber)
	k.ino = uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return k
}
