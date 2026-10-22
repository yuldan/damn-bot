//go:build windows

package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// monetaryKey resolves junctions/subst via the handle's final path and folds
// case. Remote paths (UNC or a DRIVE_REMOTE volume) are lexical only.
func monetaryKey(p string) string {
	return keyFor(p, pathOps{remote: remotePath(p), eval: filepath.EvalSymlinks, final: finalPath, fold: strings.ToLower})
}

// remotePath checks the volume root lexically for UNC and drive type; it
// never touches a share (SEC10-19).
func remotePath(p string) bool {
	p = cleanDevicePrefix(p)
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	vol := filepath.VolumeName(p)
	if len(vol) < 2 {
		return false
	}
	return windows.GetDriveType(windows.StringToUTF16Ptr(vol+`\`)) == windows.DRIVE_REMOTE
}

// finalPath returns the canonical path of an existing local file or
// directory via GetFinalPathNameByHandle; "" when it cannot be opened.
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
