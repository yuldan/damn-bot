package sandbox

import (
	"path"
	"regexp"
	"strings"
)

// msysDrive matches Git Bash/MSYS/Cygwin drive paths: /c/Users, /cygdrive/c.
var msysDrive = regexp.MustCompile(`^/(?:cygdrive/)?([a-zA-Z])(?:/|$)`)

// normalizeWindows rewrites a Windows path spelling to its plain DOS form,
// independent of the host OS so it is testable everywhere:
//   - MSYS /c/Users/x → C:\Users\x (EC-WIN-012);
//   - "/" → `\`;
//   - `\\?\C:\x` and `\??\C:\x` → C:\x, `\\?\UNC\srv\share` → `\\srv\share`
//     (EC-WIN-010: Go re-adds the long-path prefix itself when opening).
//
// Other `\\?\` forms (volume GUIDs) are kept: they never prefix-match a
// root, which fails closed (OutsideRoot).
func normalizeWindows(p string) string {
	if m := msysDrive.FindStringSubmatchIndex(p); m != nil {
		p = strings.ToUpper(p[m[2]:m[3]]) + `:\` + p[m[1]:]
	}
	p = strings.ReplaceAll(p, "/", `\`)
	for _, pre := range []string{`\\?\`, `\??\`} {
		if !strings.HasPrefix(p, pre) {
			continue
		}
		rest := p[len(pre):]
		switch {
		case len(rest) >= 4 && strings.EqualFold(rest[:4], `UNC\`):
			return `\\` + rest[4:]
		case len(rest) >= 2 && rest[1] == ':' && isASCIILetter(rest[0]):
			return rest
		}
	}
	return p
}

func isASCIILetter(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }

// windowsKey is the identity of a Windows path (EC-WIN-013): normalized
// spelling, lower case, single separators, no trailing separator or "."
// components.
func windowsKey(p string) string {
	p = strings.ToLower(strings.ReplaceAll(normalizeWindows(p), `\`, "/"))
	unc := strings.HasPrefix(p, "//")
	p = path.Clean(p)
	if unc {
		p = "/" + p
	}
	if len(p) == 2 && p[1] == ':' {
		p += "/"
	}
	return p
}

// sameWindowsPath reports whether a and b name the same Windows path.
func sameWindowsPath(a, b string) bool { return windowsKey(a) == windowsKey(b) }
