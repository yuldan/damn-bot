package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocaleFromFile pins the pre-load [ui].language probe: valid values
// resolve, everything else falls back to English.
func TestLocaleFromFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tt := range []struct {
		name, body, want string
	}{
		{"id", "[ui]\nlanguage = \"id\"\n", "id"},
		{"auto", "[ui]\nlanguage = \"auto\"\n", "en"},
		{"unknown", "[ui]\nlanguage = \"xx\"\n", "en"},
		{"regional", "[ui]\nlanguage = \"pt-BR\"\n", "pt"},
		{"empty", "[ui]\ntheme = \"mono\"\n", "en"},
		{"invalid", "[ui\nlanguage = \"id\"\n", "en"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, tt.name+".toml")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			if got := localeFromFile(path); got != tt.want {
				t.Errorf("localeFromFile(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
	if got := localeFromFile(filepath.Join(dir, "missing.toml")); got != "en" {
		t.Errorf("missing config = %q, want en", got)
	}
}

// TestHelpListEnglish locks the default help screen to the pre-i18n text: an
// English locale must not change a byte of the usage output.
func TestHelpListEnglish(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writeSurfaceList(&buf, "en")
	out := buf.String()
	for _, want := range []string{
		"usage: damnbot <command> [flags]\n\nCommands:\n",
		"\nGlobal flags:\n",
		"  --profile <name>  use a separate config and data profile\n",
		"  -h, --help        print help for a command\n",
		"  --version         print the version (alias of `damnbot version`)\n",
		"\nRun `damnbot help <command>` for details.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help output missing %q:\n%s", want, out)
		}
	}
}
