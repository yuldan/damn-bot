package archtest

import (
	"strings"
	"testing"
)

// TestF8CSPNoSink is the GAP-SEC-06 "0 sink HTML" hook: the web UI never
// builds markup from strings (no innerHTML/document.write/eval/new Function)
// and the Go side never opts out of html/template escaping.
func TestF8CSPNoSink(t *testing.T) {
	t.Parallel()
	forbiddenJS := []string{"innerHTML", "outerHTML", "document.write", "eval(", "new Function", "insertAdjacentHTML"}
	scripts := []string{
		"internal/web/assets/app.js",
		"internal/web/assets/login.js",
		"internal/web/assets/settings.js",
	}
	for _, name := range scripts {
		src := readRepoFile(t, name)
		for _, bad := range forbiddenJS {
			if strings.Contains(src, bad) {
				t.Errorf("%s uses %q (DOM builder required)", name, bad)
			}
		}
	}
	forbiddenGo := []string{"template.HTML", "template.JS", "template.URL", "template.HTMLAttr", "text/template"}
	packages := []string{
		"internal/web/server.go",
		"internal/web/security.go",
		"internal/web/settings.go",
		"internal/web/assets.go",
		"internal/web/sse.go",
		"internal/web/auth.go",
	}
	for _, name := range packages {
		src := readRepoFile(t, name)
		for _, bad := range forbiddenGo {
			if strings.Contains(src, bad) {
				t.Errorf("%s uses %q (escaping bypass)", name, bad)
			}
		}
	}
}
