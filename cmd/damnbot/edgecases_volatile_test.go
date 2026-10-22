package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestVolatileTimeIsUTC (EC-FIO-019): the volatile clock is rendered in UTC, so
// a locale-provided zone name (possibly non-ASCII or holding an encoded
// surrogate) never reaches the prompt.
//
// It lives here, not in internal/assemble, because that package grants its
// tests the standard library and internal/loop/event only (E-01,
// TestImportsAllowed), while a catalog row needs the phase package; the
// assertion uses the exported surface only.
func TestVolatileTimeIsUTC(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-FIO-019", "F1", "covered") {
		return
	}
	t.Run("EC-FIO-019", func(t *testing.T) {
		t.Parallel()
		asm, err := assemble.New(assemble.Deps{Tokens: budgetCounter{}, Scrub: func(s string) string { return s }})
		if err != nil {
			t.Fatalf("assemble.New: %v", err)
		}
		for _, name := range []string{"日本", "Europa/Zürich", "\xed\xa0\x80"} {
			now := time.Date(2026, 10, 19, 23, 5, 0, 0, time.FixedZone(name, 9*3600))
			got := asm.Freeze(&assemble.VolatileInput{Now: now}).Volatile().Time
			if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
				t.Fatalf("zone %q rendered %q", name, got)
			}
			if !strings.Contains(got, "2026-10-19 14:05 UTC") {
				t.Fatalf("zone %q rendered %q, want the UTC stamp", name, got)
			}
		}
	})
}
