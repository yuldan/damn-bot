//go:build !minimal

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/i18n"
)

// wiredKeys is every catalog key the CLI (and the web auth seam) names. The
// test proves each one exists and renders in all 18 locales.
func wiredKeys() []string {
	return []string{
		i18n.KeyHelpUsage, i18n.KeyHelpCommands, i18n.KeyHelpGlobalFlags,
		i18n.KeyHelpProfile, i18n.KeyHelpHelp, i18n.KeyHelpVersion,
		i18n.KeyHelpDetails, i18n.KeyHelpUnknownTopic, i18n.KeyHelpUnknownHint,
		i18n.KeyHelpTopicHeader, i18n.KeyConfigLoad, i18n.KeyAPIAuthRequired,
		i18n.KeyWebAuthUnauthorized,
	}
}

// TestCLIUsedKeysExist enforces 0 missing keys for every string the CLI uses.
func TestCLIUsedKeysExist(t *testing.T) {
	t.Parallel()
	for _, locale := range i18n.Locales() {
		for _, key := range wiredKeys() {
			if got := i18n.T(locale, key); got == "" || got == key {
				t.Errorf("%s: key %q is missing from the catalog", locale, key)
			}
		}
	}
}

// TestHelpListLocalized proves help renders the configured language.
func TestHelpListLocalized(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writeSurfaceList(&buf, "id")
	out := buf.String()
	for _, want := range []string{"penggunaan: damnbot <perintah> [flag]", "Perintah:", "Flag global:"} {
		if !strings.Contains(out, want) {
			t.Errorf("localized help missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Commands:") {
		t.Errorf("localized help kept the English heading:\n%s", out)
	}
}

// TestConfigLoadErrorLocalized checks that [ui].language is honored even when
// the rest of the file is invalid, so the failure itself is translated.
func TestConfigLoadErrorLocalized(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[ui]\nlanguage = \"id\"\ntheme = \"solarized\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	loader, err := config.New(config.Deps{
		ReadFile: os.ReadFile,
		Getenv:   func(string) string { return "" },
		HomeDir:  func() (string, error) { return t.TempDir(), nil },
		GOOS:     runtime.GOOS,
	})
	if err != nil {
		t.Fatalf("config.New: %v", err)
	}
	_, err = loadRunConfig(loader, path)
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	if !strings.HasPrefix(err.Error(), "muat konfigurasi: ") {
		t.Errorf("config error = %q, want the Indonesian prefix", err)
	}
}

// TestAPITokenErrorLocalized covers the web API auth message, including the
// English fallback for "auto".
func TestAPITokenErrorLocalized(t *testing.T) {
	t.Parallel()
	const english = "bearer token required: set DAMNBOT_API_TOKEN or --token-secret (or pass --allow-unauthenticated explicitly)"
	if got := apiTokenError("auto").Error(); got != english {
		t.Errorf("auto = %q, want the English message", got)
	}
	if got := apiTokenError("id").Error(); got == english || !strings.Contains(got, "token bearer diperlukan") {
		t.Errorf("id = %q, want the Indonesian message", got)
	}
}

// TestWebAuthSeamExists pins the web-UI catalog seam: internal/web is not in
// this tree yet, so the key is the contract the package will consume.
func TestWebAuthSeamExists(t *testing.T) {
	t.Parallel()
	for _, locale := range i18n.Locales() {
		got := i18n.T(locale, i18n.KeyWebAuthUnauthorized)
		if got == "" || got == i18n.KeyWebAuthUnauthorized {
			t.Errorf("%s: web auth message missing", locale)
		}
	}
}
