package builtin

import (
	"bytes"
	"path/filepath"
	"strings"
)

// Warning is one non-blocking security-guidance finding on a write result
// (PLUG-025). The write already happened; the caller only appends the warning.
type Warning struct {
	Code    string
	Message string
}

// WriteResult is the unchanged write outcome plus guidance warnings.
type WriteResult struct {
	Path     string
	Bytes    int
	Warnings []Warning
}

type guidanceRule struct {
	code    string
	message string
	match   func(path string, content []byte) bool
}

func guidanceRules() []guidanceRule {
	return []guidanceRule{
		{"env-file", "write to a .env file may expose credentials", func(path string, _ []byte) bool {
			return strings.HasSuffix(strings.ToLower(path), ".env") || strings.Contains(strings.ToLower(path), string(filepath.Separator)+".env")
		}},
		{"private-key", "file contains a private key block", func(_ string, content []byte) bool {
			return bytes.Contains(content, []byte("PRIVATE KEY-----"))
		}},
		{"curl-pipe-shell", "content pipes a download into a shell", func(_ string, content []byte) bool {
			s := string(content)
			return strings.Contains(s, "| sh") || strings.Contains(s, "| bash")
		}},
		{"world-writable", "content grants world-writable permissions", func(_ string, content []byte) bool {
			return bytes.Contains(content, []byte("chmod 777")) || bytes.Contains(content, []byte("chmod -R 777"))
		}},
		{"recursive-delete", "content contains a recursive forced delete", func(_ string, content []byte) bool {
			return bytes.Contains(content, []byte("rm -rf /")) || bytes.Contains(content, []byte("sudo rm -rf"))
		}},
	}
}

// AfterWrite inspects a completed write and returns warnings. It never returns
// an error and never blocks or undoes the write.
func AfterWrite(path string, content []byte, written int) WriteResult {
	res := WriteResult{Path: path, Bytes: written}
	for _, r := range guidanceRules() {
		if r.match(path, content) {
			res.Warnings = append(res.Warnings, Warning{Code: r.code, Message: r.message})
		}
	}
	return res
}
