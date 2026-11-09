//go:build !minimal

package i18n

import (
	"slices"
	"testing"
)

// TestI18nRegisteredLocales is the SPEC §9.12/Q16 count gate: the full build
// ships exactly the seventeen hermes parity languages plus Indonesian.
func TestI18nRegisteredLocales(t *testing.T) {
	t.Parallel()
	want := []string{
		"af", "ar", "de", "en", "es", "fr", "ga", "hu", "id", "it",
		"ja", "ko", "pt", "ru", "tr", "uk", "zh", "zh-hant",
	}
	if got := Locales(); !slices.Equal(got, want) {
		t.Fatalf("Locales() = %v, want %v", got, want)
	}
	if n := len(Locales()); n != 18 {
		t.Fatalf("registered locales = %d, want 18", n)
	}
	if !slices.Contains(Locales(), "id") {
		t.Error("Indonesian (Q16) is not registered")
	}
}
