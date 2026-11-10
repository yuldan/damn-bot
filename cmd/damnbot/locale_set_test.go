//go:build !minimal

package main

import (
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/i18n"
	"github.com/damnbot/damnbot/internal/web"
)

// TestLocaleSetIdentical is GAP-INF-08: the config validator (CLI/TUI/gateway
// path), the web UI and the embedded catalog expose exactly one §9.12 locale
// set. A surface that grows or loses a locale fails here instead of drifting.
// It lives in the composition root because the import matrix forbids direct
// config → i18n and web → config edges.
func TestLocaleSetIdentical(t *testing.T) {
	t.Parallel()
	catalog := i18n.Locales()
	if !slices.Equal(web.SupportedLocales(), catalog) {
		t.Errorf("web locales = %v, catalog = %v", web.SupportedLocales(), catalog)
	}
	for _, code := range catalog {
		cfg := config.Default()
		cfg.UI.Language = code
		if err := cfg.Validate(); err != nil {
			t.Errorf("config rejects catalog locale %q: %v", code, err)
		}
	}
	cfg := config.Default()
	cfg.UI.Language = "auto"
	if err := cfg.Validate(); err != nil {
		t.Errorf("config rejects auto: %v", err)
	}
	cfg.UI.Language = "xx"
	if err := cfg.Validate(); err == nil {
		t.Error("config accepted a locale outside the catalog")
	}
}
