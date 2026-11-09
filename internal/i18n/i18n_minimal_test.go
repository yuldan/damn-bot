//go:build minimal

package i18n

import (
	"slices"
	"testing"
)

// TestI18nMinimalEnglishOnly: the minimal build ships English only and every
// other locale resolves to it (size gate headroom, catalog_full.go).
func TestI18nMinimalEnglishOnly(t *testing.T) {
	t.Parallel()
	if got := Locales(); !slices.Equal(got, []string{Default}) {
		t.Fatalf("Locales() = %v, want [en]", got)
	}
	if got := Resolve("id"); got != Default {
		t.Errorf("Resolve(id) = %q, want en", got)
	}
	if got, want := T("id", KeyHelpUsage), T(Default, KeyHelpUsage); got != want {
		t.Errorf("T(id) = %q, want %q", got, want)
	}
	if err := Validate(); err != nil {
		t.Fatalf("embedded catalog invalid: %v", err)
	}
}
