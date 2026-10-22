//go:build windows

package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// monetaryKey expands 8.3 short names to their long form and folds case.
func monetaryKey(p string) string {
	k := canonicalBase(p)
	if long := longPath(k); long != "" {
		k = long
	}
	return strings.ToLower(k)
}

// longPath returns the long form of an existing path, or "".
func longPath(p string) string {
	ptr, err := windows.UTF16PtrFromString(filepath.FromSlash(p))
	if err != nil {
		return ""
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetLongPathName(ptr, &buf[0], uint32(len(buf)))
	if err == nil && n > 0 && n < uint32(len(buf)) {
		return filepath.ToSlash(windows.UTF16ToString(buf[:n]))
	}
	if n <= uint32(len(buf)) {
		return ""
	}
	buf = make([]uint16, n+1)
	if n, err = windows.GetLongPathName(ptr, &buf[0], uint32(len(buf))); err != nil || n == 0 || n >= uint32(len(buf)) {
		return ""
	}
	return filepath.ToSlash(windows.UTF16ToString(buf[:n]))
}
