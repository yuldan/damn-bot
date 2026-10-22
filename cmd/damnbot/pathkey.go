package main

import (
	"path/filepath"
)

// existingPrefix resolves the longest existing prefix of p and returns it
// with the non-existing remainder (SEC10-18). The prefix is absolute and
// symlink-resolved; the remainder keeps its spelling for the OS wrapper.
func existingPrefix(p string) (base, rest string) {
	abs, err := filepath.Abs(cleanDevicePrefix(p))
	if err != nil {
		abs = p
	}
	abs = filepath.Clean(abs)
	for {
		if r, rerr := filepath.EvalSymlinks(abs); rerr == nil {
			return r, rest
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return abs, rest
		}
		rest = filepath.Join(filepath.Base(abs), rest)
		abs = parent
	}
}

// cleanDevicePrefix strips Win32 device prefixes for canonicalization;
// SEC10-15 (e.g. \\localhost\C$) remains a documented residual.
func cleanDevicePrefix(p string) string {
	switch {
	case len(p) >= 8 && p[:8] == `\\?\UNC\`:
		return `\\` + p[8:]
	case len(p) >= 4 && (p[:4] == `\\?\` || p[:4] == `\\.\`):
		return p[4:]
	}
	return p
}

// canonicalBase is the OS-neutral key: absolute, longest existing prefix
// symlink-resolved, remainder appended, slash-separated.
func canonicalBase(p string) string {
	base, rest := existingPrefix(p)
	return filepath.ToSlash(filepath.Join(base, rest))
}
