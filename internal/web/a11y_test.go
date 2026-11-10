package web

import (
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var cssColorVar = regexp.MustCompile(`--([a-z]+):\s*#([0-9a-fA-F]{6})`)

// cssVars reads the :root colour tokens of the embedded stylesheet.
func cssVars(t *testing.T) map[string]string {
	t.Helper()
	assets, err := defaultAssets()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(assets, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{}
	for _, m := range cssColorVar.FindAllStringSubmatch(string(raw), -1) {
		vars[m[1]] = m[2]
	}
	if len(vars) < 6 {
		t.Fatalf("css tokens = %v", vars)
	}
	return vars
}

// channel linearises one 8-bit sRGB component.
func channel(hex string, i int) float64 {
	v, err := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
	if err != nil {
		return 0
	}
	c := float64(v) / 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// luminance is the WCAG 2.x relative luminance.
func luminance(hex string) float64 {
	return 0.2126*channel(hex, 0) + 0.7152*channel(hex, 1) + 0.0722*channel(hex, 2)
}

// contrast is the WCAG contrast ratio between two #rrggbb colours.
func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestContrastTokens is GAP-UX-07: every text token reaches WCAG 2.2 AA
// (>= 4.5:1) and the accent used for UI boundaries reaches 3:1.
func TestContrastTokens(t *testing.T) {
	t.Parallel()
	vars := cssVars(t)
	for _, pair := range [][2]string{
		{"fg", "bg"},
		{"muted", "bg"},
		{"accent", "bg"},
		{"fg", "card"},
		{"muted", "card"},
		{"accent", "card"},
	} {
		got := contrast(vars[pair[0]], vars[pair[1]])
		if got < 4.5 {
			t.Errorf("--%s on --%s = %.2f:1, want >= 4.5", pair[0], pair[1], got)
		}
	}
	if got := contrast(vars["accent"], vars["bg"]); got < 3 {
		t.Errorf("accent boundary = %.2f:1, want >= 3", got)
	}
}

// TestF8WebA11y is GAP-UX-07: semantic landmarks and live regions, visible
// keyboard focus, 44 px targets, reduced-motion support and per-locale lang/dir.
func TestF8WebA11y(t *testing.T) {
	t.Parallel()
	assets, err := defaultAssets()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(assets, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, want := range []string{":focus-visible", "prefers-reduced-motion", "min-height: 44px", "min-width: 44px", "max-width: 400px"} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	srv, ts := newTestServer(t, func(c *Config) { c.Language = "ar" })
	session, _ := signIn(t, srv, ts)
	page := get(t, ts, "/", session)
	if page.status != 200 {
		t.Fatalf("dashboard = %d", page.status)
	}
	for _, want := range []string{
		`lang="ar" dir="rtl"`,
		`role="log"`,
		`aria-live="polite"`,
		`role="status"`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	if got := strings.Count(page.body, "<main"); got != 1 {
		t.Errorf("<main> landmarks = %d, want 1", got)
	}
	if got := strings.Count(page.body, "<h1"); got != 1 {
		t.Errorf("<h1> headings = %d, want 1", got)
	}
	for _, path := range []string{"/settings", "/auth"} {
		body := get(t, ts, path, session).body
		if !strings.Contains(body, `lang="ar" dir="rtl"`) {
			t.Errorf("%s lacks lang/dir", path)
		}
	}

	// Automated axe-like audit over all 18 locales; only serious/critical
	// findings fail (GAP-UX-07).
	violations, err := auditEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if got := severe(violations); len(got) != 0 {
		t.Fatalf("a11y serious/critical findings: %+v", got)
	}
}
