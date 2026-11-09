//go:build !minimal

package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/i18n"
)

var (
	htmlTextNode   = regexp.MustCompile(`(?s)>([^<]+)<`)
	htmlComment    = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlUITextAttr = regexp.MustCompile(`(?i)\s(?:title|placeholder|aria-label|alt)="([^"]*)"`)
	htmlAction     = regexp.MustCompile(`^\{\{.*\}\}$`)
	jsQuotedString = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`)
)

// TestNoUILiterals is GAP-INF-08: every visible string in the embedded HTML
// templates comes from the catalog, and the ES modules carry no user-facing
// sentence literal (they read data-msg-* attributes instead).
func TestNoUILiterals(t *testing.T) {
	t.Parallel()
	assets, err := defaultAssets()
	if err != nil {
		t.Fatal(err)
	}
	checkHTMLNoLiterals(t, assets)
	checkJSNoLiterals(t, assets)
}

// checkHTMLNoLiterals rejects visible HTML text and UI attributes that do not
// come from the catalog.
func checkHTMLNoLiterals(t *testing.T, assets fs.FS) {
	t.Helper()
	pages, err := fs.Glob(assets, "*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("no HTML templates found")
	}
	for _, name := range pages {
		raw, rerr := fs.ReadFile(assets, name)
		if rerr != nil {
			t.Fatalf("%s: %v", name, rerr)
		}
		body := htmlComment.ReplaceAllString(string(raw), "")
		for _, m := range htmlTextNode.FindAllStringSubmatch(body, -1) {
			if text := strings.TrimSpace(m[1]); text != "" && !htmlAction.MatchString(text) {
				t.Errorf("%s: literal UI text %q must come from the catalog", name, text)
			}
		}
		for _, m := range htmlUITextAttr.FindAllStringSubmatch(body, -1) {
			if value := strings.TrimSpace(m[1]); value != "" && !strings.Contains(value, "{{") {
				t.Errorf("%s: literal %q attribute must come from the catalog", name, value)
			}
		}
	}
}

// checkJSNoLiterals rejects quoted sentence literals in the ES modules; the
// copy must arrive through data-msg-* attributes.
func checkJSNoLiterals(t *testing.T, assets fs.FS) {
	t.Helper()
	scripts, err := fs.Glob(assets, "*.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range scripts {
		raw, rerr := fs.ReadFile(assets, name)
		if rerr != nil {
			t.Fatalf("%s: %v", name, rerr)
		}
		for _, literal := range jsQuotedString.FindAllString(string(raw), -1) {
			if text := strings.Trim(literal, `"'`); strings.ContainsAny(text, " \t") {
				t.Errorf("%s: quoted UI literal %q must come from data-msg-* attributes", name, text)
			}
		}
	}
}

// TestSettingsWritableLocalized is UI-023/GAP-INF-08: a build with a settings
// writer renders the localized preview/apply form in every locale, so the
// #847 preview/apply surface does not regress to English copy.
func TestSettingsWritableLocalized(t *testing.T) {
	t.Parallel()
	locales := SupportedLocales()
	for _, locale := range locales {
		t.Run(locale, func(t *testing.T) {
			t.Parallel()
			srv, ts := newTestServer(t, func(c *Config) {
				c.Language = locale
				c.Locales = locales
				c.Settings = &fakeSettings{}
			})
			session, _ := signIn(t, srv, ts)
			page := get(t, ts, "/settings", session)
			if page.status != http.StatusOK {
				t.Fatalf("settings = %d", page.status)
			}
			if !strings.Contains(page.body, `id="settings-form"`) ||
				!strings.Contains(page.body, `id="language-select"`) {
				t.Error("writable settings lacks the preview form")
			}
			preview := i18n.T(locale, "web.preview")
			apply := i18n.T(locale, "web.apply")
			if !strings.Contains(page.body, preview) {
				t.Errorf("settings lacks the %s preview button %q", locale, preview)
			}
			if !strings.Contains(page.body, apply) {
				t.Errorf("settings lacks the %s apply button %q", locale, apply)
			}
			if strings.Contains(page.body, i18n.T("en", "web.readonly")) {
				t.Error("writable settings renders the read-only fallback")
			}
		})
	}
}

// TestSettingsReadOnlyFallbackLocalized is UI-023/GAP-INF-08: a build without
// a settings writer keeps the read-only page, localized through web.readonly,
// and still lists every catalog locale.
func TestSettingsReadOnlyFallbackLocalized(t *testing.T) {
	t.Parallel()
	locales := SupportedLocales()
	for _, locale := range locales {
		t.Run(locale, func(t *testing.T) {
			t.Parallel()
			srv, ts := newTestServer(t, func(c *Config) {
				c.Language = locale
				c.Locales = locales
			})
			session, _ := signIn(t, srv, ts)
			page := get(t, ts, "/settings", session)
			if page.status != http.StatusOK {
				t.Fatalf("settings = %d", page.status)
			}
			if strings.Contains(page.body, `id="settings-form"`) {
				t.Error("read-only settings renders the writable form")
			}
			if want := i18n.T(locale, "web.readonly"); !strings.Contains(page.body, want) {
				t.Errorf("settings lacks the %s read-only notice %q", locale, want)
			}
			for _, code := range locales {
				if !strings.Contains(page.body, "<li>"+code+"</li>") {
					t.Errorf("settings does not list locale %q", code)
				}
			}
		})
	}
}

// TestF8UI023I18n is UI-023: the web pages render each of the 18 locales with
// the right lang/dir, translated catalog strings, and an identical locale set.
func TestF8UI023I18n(t *testing.T) {
	t.Parallel()
	locales := SupportedLocales()
	if len(locales) != 18 {
		t.Fatalf("SupportedLocales() = %v, want 18 locales", locales)
	}
	for _, locale := range locales {
		t.Run(locale, func(t *testing.T) {
			t.Parallel()
			checkLocalePages(t, locale, locales)
		})
	}
}

// checkLocalePages renders both authenticated pages for one locale.
func checkLocalePages(t *testing.T, locale string, locales []string) {
	t.Helper()
	srv, ts := newTestServer(t, func(c *Config) { c.Language = locale })
	session, _ := signIn(t, srv, ts)
	checkDashboardLocale(t, ts, session, locale)
	checkSettingsLocaleList(t, ts, session, locales)
}

// checkDashboardLocale asserts lang/dir and translated copy on the dashboard.
func checkDashboardLocale(t *testing.T, ts *httptest.Server, session *http.Cookie, locale string) {
	t.Helper()
	page := get(t, ts, "/", session)
	if page.status != http.StatusOK {
		t.Fatalf("dashboard = %d", page.status)
	}
	if want := `lang="` + locale + `" dir="` + dirOf(locale) + `"`; !strings.Contains(page.body, want) {
		t.Errorf("dashboard lacks %s", want)
	}
	if !strings.Contains(page.body, i18n.T(locale, "web.session")) {
		t.Errorf("dashboard does not carry the %s translation", locale)
	}
	if locale != "en" && !pageCopyDistinct(locale) {
		t.Errorf("%s: page copy is still English", locale)
	}
}

// pageCopyDistinct reports whether the locale translates the sampled dashboard
// keys instead of copying English.
func pageCopyDistinct(locale string) bool {
	for _, key := range []string{"web.settings", "web.sign_out", "web.waiting", "web.language"} {
		if i18n.T(locale, key) != i18n.T("en", key) {
			return true
		}
	}
	return false
}

// checkSettingsLocaleList asserts the settings page lists every catalog locale.
func checkSettingsLocaleList(t *testing.T, ts *httptest.Server, session *http.Cookie, locales []string) {
	t.Helper()
	settings := get(t, ts, "/settings", session)
	if settings.status != http.StatusOK {
		t.Fatalf("settings = %d", settings.status)
	}
	for _, code := range locales {
		if !strings.Contains(settings.body, "<li>"+code+"</li>") {
			t.Errorf("settings does not list locale %q", code)
		}
	}
}
