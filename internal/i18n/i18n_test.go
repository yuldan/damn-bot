//go:build !minimal

package i18n

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// usedKeys is the catalog surface wired by the CLI (and the web auth seam).
// Every key must exist in every locale: a typo in a call site fails here.
func usedKeys() []string {
	return []string{
		KeyHelpUsage, KeyHelpCommands, KeyHelpGlobalFlags, KeyHelpProfile,
		KeyHelpHelp, KeyHelpVersion, KeyHelpDetails, KeyHelpUnknownTopic,
		KeyHelpUnknownHint, KeyHelpTopicHeader, KeyConfigLoad,
		KeyAPIAuthRequired, KeyWebAuthUnauthorized,
		"web.title", "web.settings", "web.dashboard", "web.sign_out",
		"web.session", "web.events", "web.connecting", "web.waiting",
		"web.signed_in", "web.expired", "web.stream_open", "web.reconnecting",
		"web.language", "web.current", "web.theme", "web.theme_note",
		"web.signin", "web.signing_in", "web.no_nonce", "web.link_used",
		"web.unreachable", "web.auto", "web.preview", "web.apply",
		"web.readonly", "web.preview_refused", "web.review_diff",
		"web.applied", "web.apply_expired",
		"web.panes", "web.projects", "web.pane_kind", "web.pane_target",
		"web.pane_open", "web.pane_close", "web.pane_refused", "web.pane_closed",
		"web.terminal",
	}
}

// TestI18nNoMissingKeys enforces the SPEC §9.12 invariant: every locale has
// exactly the English key set (0 missing, 0 foreign), and every wired key is
// present.
func TestI18nNoMissingKeys(t *testing.T) {
	t.Parallel()
	catalogs, err := parseCatalogs()
	if err != nil {
		t.Fatalf("load embedded catalogs: %v", err)
	}
	base := catalogs[Default]
	if len(base) == 0 {
		t.Fatal("en catalog is empty")
	}
	want := slices.Sorted(maps.Keys(base))
	if got := Keys(); !slices.Equal(got, want) {
		t.Errorf("Keys() = %v, want %v", got, want)
	}
	for _, locale := range Locales() {
		messages := catalogs[locale]
		if len(messages) != len(base) {
			t.Errorf("%s: %d keys, want %d", locale, len(messages), len(base))
		}
		for _, key := range want {
			if _, ok := messages[key]; !ok {
				t.Errorf("%s: missing key %q", locale, key)
			}
		}
		for key := range messages {
			if _, ok := base[key]; !ok {
				t.Errorf("%s: unknown key %q", locale, key)
			}
		}
	}
	for _, key := range usedKeys() {
		if _, ok := base[key]; !ok {
			t.Errorf("wired key %q is missing from the catalog", key)
		}
	}
}

// TestLocaleNotEnglishCopy is GAP-INF-08: the settings surface is genuinely
// translated, not an English copy. The product name (`web.title`) is exempt
// because a brand is the same everywhere; every settings key must differ.
func TestLocaleNotEnglishCopy(t *testing.T) {
	t.Parallel()
	catalogs, err := parseCatalogs()
	if err != nil {
		t.Fatalf("load embedded catalogs: %v", err)
	}
	base := catalogs[Default]
	settingsKeys := []string{
		"web.auto", "web.preview", "web.apply", "web.readonly",
		"web.preview_refused", "web.review_diff", "web.applied",
		"web.apply_expired",
	}
	for _, locale := range Locales() {
		if locale == Default {
			continue
		}
		for _, key := range settingsKeys {
			if catalogs[locale][key] == base[key] {
				t.Errorf("%s: %q still carries the English copy %q", locale, key, base[key])
			}
		}
	}
}

// TestI18nLocales checks that the registered list is stable, sorted, and
// unique; the exact 18-locale contract lives in TestI18nRegisteredLocales.
func TestI18nLocales(t *testing.T) {
	t.Parallel()
	locales := Locales()
	if !slices.IsSorted(locales) {
		t.Errorf("Locales() = %v, want lexical order", locales)
	}
	if !slices.Contains(locales, Default) {
		t.Errorf("Locales() = %v, missing %q", locales, Default)
	}
	for i, locale := range locales[1:] {
		if locale == locales[i] {
			t.Errorf("Locales() = %v, duplicate %q", locales, locale)
		}
	}
}

// TestI18nResolve pins the config-value mapping: auto/unknown → en, case is
// ignored, and a regional tag uses its registered primary subtag.
func TestI18nResolve(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in, want string
	}{
		{"", Default},
		{"auto", Default},
		{"AUTO", Default},
		{"en", "en"},
		{"ID", "id"},
		{"zh-hant", "zh-hant"},
		{"ZH-Hant", "zh-hant"},
		{"pt-BR", "pt"},
		{"en-GB", "en"},
		{"xx", Default},
		{"x-default", Default},
	} {
		if got := Resolve(tt.in); got != tt.want {
			t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestI18nFallback checks the fallback chain: locale → en → key.
func TestI18nFallback(t *testing.T) {
	t.Parallel()
	fixture := catalog{
		"en": {"greet": "hello %s", "only.en": "english"},
		"de": {"greet": "hallo %s"},
	}
	if got := message(fixture, "de", "greet"); got != "hallo %s" {
		t.Errorf("lookup(de, greet) = %q", got)
	}
	if got := message(fixture, "fr", "greet"); got != "hello %s" {
		t.Errorf("missing locale text = %q, want the en fallback", got)
	}
	if got := message(fixture, "xx", "only.en"); got != "english" {
		t.Errorf("unknown locale = %q, want the en fallback", got)
	}
	if got := message(fixture, "de", "no.such.key"); got != "no.such.key" {
		t.Errorf("unknown key = %q, want the key itself", got)
	}
	if got := T("xx", KeyHelpUsage); got != T(Default, KeyHelpUsage) {
		t.Errorf("T(xx) = %q, want T(en)", got)
	}
	if got := T("de", "no.such.key"); got != "no.such.key" {
		t.Errorf("T(de, no.such.key) = %q, want the key", got)
	}
}

// TestI18nFormat checks that translated placeholders survive every locale and
// that a missing key with args is not run through Sprintf.
func TestI18nFormat(t *testing.T) {
	t.Parallel()
	for _, locale := range Locales() {
		quoted := T(locale, KeyHelpUnknownTopic, "bogus")
		if strings.Contains(quoted, "%!") || !strings.Contains(quoted, `"bogus"`) {
			t.Errorf("%s: KeyHelpUnknownTopic = %q", locale, quoted)
		}
		header := T(locale, KeyHelpTopicHeader, "sessions", "summary")
		if !strings.Contains(header, "sessions") || !strings.Contains(header, "summary") {
			t.Errorf("%s: KeyHelpTopicHeader = %q", locale, header)
		}
	}
	if got := T("en", "unknown.key", "ignored"); got != "unknown.key" {
		t.Errorf("missing key with args = %q, want the key", got)
	}
}

// TestI18nValidateRejectsBadCatalog proves the validator catches every way a
// catalog can break the contract.
func TestI18nValidateRejectsBadCatalog(t *testing.T) {
	t.Parallel()
	locales := []string{"en", "de"}
	good := func() catalog {
		return catalog{
			"en": {"a": "one %s", "b": "two"},
			"de": {"a": "eins %s", "b": "zwei"},
		}
	}
	if err := validateCatalogs(good(), locales); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(catalog)
	}{
		{"missing key", func(c catalog) { delete(c["de"], "b") }},
		{"extra key", func(c catalog) { c["de"]["c"] = "drei" }},
		{"empty value", func(c catalog) { c["de"]["b"] = "  " }},
		{"verb mismatch", func(c catalog) { c["de"]["a"] = "eins" }},
		{"missing locale", func(c catalog) { delete(c, "de") }},
		{"missing default", func(c catalog) { delete(c, "en") }},
		{"unexpected locale", func(c catalog) { c["fr"] = map[string]string{"a": "un %s", "b": "deux"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := good()
			tt.mutate(c)
			if err := validateCatalogs(c, locales); err == nil {
				t.Error("invalid catalog accepted")
			}
		})
	}
}

// TestI18nCatalogValid is the named embedded-data gate.
func TestI18nCatalogValid(t *testing.T) {
	t.Parallel()
	if err := Validate(); err != nil {
		t.Fatalf("embedded catalog invalid: %v", err)
	}
}

// criticalPrefix is the catalog namespace whose keys may never be empty: the
// approval, preview, error, destructive, budget and SAFE MODE surfaces
// (GAP-INF-08). The prefix is the marker; a new critical surface must use it.
const criticalPrefix = "critical."

// TestI18nNoEmpty is GAP-INF-08: no locale ships an empty value.
func TestI18nNoEmpty(t *testing.T) {
	t.Parallel()
	catalogs, err := parseCatalogs()
	if err != nil {
		t.Fatalf("load embedded catalogs: %v", err)
	}
	for _, locale := range Locales() {
		for key, text := range catalogs[locale] {
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s: empty value for %q", locale, key)
			}
		}
	}
}

// TestCriticalKeysAllLocales is GAP-INF-08: every critical key is filled in
// all 18 locales, and the six required surfaces are present.
func TestCriticalKeysAllLocales(t *testing.T) {
	t.Parallel()
	catalogs, err := parseCatalogs()
	if err != nil {
		t.Fatalf("load embedded catalogs: %v", err)
	}
	required := []string{
		"critical.approval.title", "critical.preview.title",
		"critical.error.title", "critical.destructive.confirm",
		"critical.budget.title", "critical.safe_mode.title",
	}
	for _, key := range required {
		if _, ok := catalogs[Default][key]; !ok {
			t.Errorf("required critical key %q is missing from the en catalog", key)
		}
	}
	for key := range catalogs[Default] {
		if !strings.HasPrefix(key, criticalPrefix) {
			continue
		}
		for _, locale := range Locales() {
			if strings.TrimSpace(catalogs[locale][key]) == "" {
				t.Errorf("%s: critical key %q is empty", locale, key)
			}
		}
	}
}
