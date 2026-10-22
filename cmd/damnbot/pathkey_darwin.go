//go:build darwin

package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// monetaryKey normalizes to NFC before folding case: the default APFS
// volume is case-insensitive and stores decomposed names (SEC10-16).
func monetaryKey(p string) string {
	p = norm.NFC.String(p)
	return keyFor(p, pathOps{remote: remotePath(p), eval: filepath.EvalSymlinks, fold: strings.ToLower})
}

func remotePath(p string) bool { return strings.HasPrefix(cleanDevicePrefix(p), `\\`) }
