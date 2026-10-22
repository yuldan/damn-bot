package main

import (
	"path/filepath"
	"strings"
)

// pathOps is the filesystem seam of the gate key. A remote path must not
// invoke eval/final at all (SEC10-19).
type pathOps struct {
	remote bool
	eval   func(string) (string, error)
	final  func(string) string
	fold   func(string) string
}

// keyFor builds the canonical comparison key. Remote paths are lexical only:
// opening an SMB/WebDAV operand before approval would leak NTLM or hang.
func keyFor(p string, o pathOps) string {
	p = cleanDevicePrefix(p)
	if o.remote {
		return o.fold(slash(filepath.Clean(p)))
	}
	base, rest := existingPrefix(p, o.eval)
	if o.final != nil {
		if f := o.final(base); f != "" {
			base = f
		}
	}
	return o.fold(slash(filepath.Join(base, rest)))
}

// existingPrefix resolves the longest existing prefix of p and returns it
// with the non-existing remainder (SEC10-18).
func existingPrefix(p string, eval func(string) (string, error)) (base, rest string) {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	abs = filepath.Clean(abs)
	for {
		if r, rerr := eval(abs); rerr == nil {
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

// cleanDevicePrefix strips Win32 device prefixes. Remote/UNC forms are
// classified Unknown by exec/classify and never reach a filesystem call.
func cleanDevicePrefix(p string) string {
	switch {
	case len(p) >= 8 && p[:8] == `\\?\UNC\`:
		return `\\` + p[8:]
	case len(p) >= 4 && (p[:4] == `\\?\` || p[:4] == `\\.\`):
		return p[4:]
	}
	return p
}

func identity(s string) string { return s }

// slash normalises separators on every OS (filepath.ToSlash is a no-op for
// backslashes on Unix, where UNC forms are simulated in tests).
func slash(p string) string { return strings.ReplaceAll(filepath.ToSlash(p), `\`, "/") }
