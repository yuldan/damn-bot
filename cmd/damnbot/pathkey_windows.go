//go:build windows

package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// monetaryKey uses the handle's final path, which resolves junctions and
// subst drives that EvalSymlinks misses (SEC10-14), then folds case. A
// non-existing tail is appended to its longest existing parent (SEC10-18).
func monetaryKey(p string) string {
	base, rest := existingPrefix(p)
	if final := finalPath(base); final != "" {
		base = final
	}
	return strings.ToLower(filepath.ToSlash(filepath.Join(base, rest)))
}

// finalPath returns the canonical path of an existing file or directory via
// GetFinalPathNameByHandle; "" when it cannot be opened.
func finalPath(p string) string {
	ptr, err := windows.UTF16PtrFromString(filepath.FromSlash(p))
	if err != nil {
		return ""
	}
	h, err := windows.CreateFile(ptr, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h) //nolint:errcheck // read-only handle
	buf := make([]uint16, 512)
	for {
		n, gerr := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if gerr != nil || n == 0 {
			return ""
		}
		if n < uint32(len(buf)) {
			return cleanDevicePrefix(windows.UTF16ToString(buf[:n]))
		}
		buf = make([]uint16, n+1)
	}
}
