package web

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"testing"
)

// TestWebAssetsNoExternalURL: no asset, template or rendered page references
// an external URL, and the font stack is system-only (GAP-UX-08).
func TestWebAssetsNoExternalURL(t *testing.T) {
	t.Parallel()
	assets, err := defaultAssets()
	if err != nil {
		t.Fatal(err)
	}
	err = fs.WalkDir(assets, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, rerr := fs.ReadFile(assets, name)
		if rerr != nil {
			return fmt.Errorf("web: read asset %s: %w", name, rerr)
		}
		if webExternalURL(string(raw)) {
			t.Errorf("%s references an external URL", name)
		}
		if path.Ext(name) == ".css" && !strings.Contains(string(raw), "system-ui") {
			t.Errorf("%s does not use the system font stack", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"index", "settings", "auth"} {
		assertNoExternal(t, assets, page)
	}
}

func assertNoExternal(t *testing.T, assets fs.FS, page string) {
	t.Helper()
	tmpl, err := parseTemplates(assets)
	if err != nil {
		t.Fatal(err)
	}
	data := &pageData{CSRF: "x", Lang: "en", Dir: "ltr", Language: "en", Locales: SupportedLocales()}
	var b strings.Builder
	tpl := tmpl.index
	switch page {
	case "settings":
		tpl = tmpl.settings
	case "auth":
		tpl = tmpl.auth
	}
	if err := tpl.Execute(&b, data); err != nil {
		t.Fatal(err)
	}
	if webExternalURL(b.String()) {
		t.Errorf("%s page references an external URL", page)
	}
}

// webExternalURL reports a http(s) or protocol-relative reference.
func webExternalURL(text string) bool {
	return strings.Contains(text, "http://") || strings.Contains(text, "https://")
}
