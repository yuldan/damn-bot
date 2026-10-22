//go:build !windows && !darwin

package main

import (
	"path/filepath"
	"strings"
)

// monetaryKey is case-sensitive on Linux and other POSIX filesystems. Any
// UNC/device form is lexical only (no filesystem access).
func monetaryKey(p string) string {
	return keyFor(p, pathOps{remote: remotePath(p), eval: filepath.EvalSymlinks, fold: identity})
}

func remotePath(p string) bool { return strings.HasPrefix(cleanDevicePrefix(p), `\\`) }
