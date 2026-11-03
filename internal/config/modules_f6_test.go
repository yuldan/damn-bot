//go:build !minimal

package config

import (
	"errors"
	"testing"
)

// f6Sections is the SPEC §3.2.4 module set whose config section is registered
// by the F6 foundation slice (ADR-309/ADR-310).
func f6Sections() []string {
	return []string{"web", "browser", "media", "voice", "computer_use"}
}

// TestF6ModuleSectionsDecodeEmpty: every section decodes with enabled = false
// and changes nothing else, so accepting the key is not a silent behaviour
// change (ADR-310).
func TestF6ModuleSectionsDecodeEmpty(t *testing.T) {
	t.Parallel()
	for _, id := range f6Sections() {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			cfg, err := parse(t, "[modules."+id+"]\nenabled = false\n")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cfg.Modules.Web.Enabled || cfg.Modules.Browser.Enabled ||
				cfg.Modules.Media.Enabled || cfg.Modules.Voice.Enabled ||
				cfg.Modules.ComputerUse.Enabled {
				t.Fatalf("enabled flags = %+v", cfg.Modules)
			}
		})
	}
}

// TestF6ModuleSectionsRejectUnknownKey: the section is strict; a foreign key
// keeps the module-unavailable sentinel instead of being ignored.
func TestF6ModuleSectionsRejectUnknownKey(t *testing.T) {
	t.Parallel()
	for _, id := range f6Sections() {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, "[modules."+id+"]\nenabled = false\nbogus = 1\n")
			ke := keyErrorOf(t, err, ErrModuleUnavailable)
			if errors.Is(err, ErrUnknownKey) {
				t.Errorf("module key error must not match ErrUnknownKey: %v", err)
			}
			if ke.Key != "modules."+id+".bogus" {
				t.Errorf("key = %q, want modules.%s.bogus", ke.Key, id)
			}
		})
	}
}

// TestF6ModuleSectionsRefuseEnable: enabling a module that has no runtime yet
// fails closed rather than becoming a no-op (ADR-310). Web and browser are
// excluded: they have runtimes (ADR-311, ADR-313).
func TestF6ModuleSectionsRefuseEnable(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"media", "voice", "computer_use"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, "[modules."+id+"]\nenabled = true\n")
			ke := keyErrorOf(t, err, ErrInvalidValue)
			if ke.Key != "modules."+id+".enabled" {
				t.Errorf("key = %q, want modules.%s.enabled", ke.Key, id)
			}
		})
	}
}

// TestModulesBrowserOrigin: browser accepts the opt-in, allowlist and local
// profile keys; an empty allowlist stays legal here and fails at Invoke
// (ADR-313).
func TestModulesBrowserOrigin(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[modules.browser]\nenabled = true\nallow_origins = [\"https://app.example\"]\nlocal_profile = true\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.Modules.Browser.Enabled || !cfg.Modules.Browser.LocalProfile ||
		len(cfg.Modules.Browser.AllowOrigins) != 1 {
		t.Fatalf("browser = %+v", cfg.Modules.Browser)
	}
	if _, err := parse(t, "[modules.browser]\nenabled = true\n"); err != nil {
		t.Fatalf("enabled without allowlist must load: %v", err)
	}
}

// TestModulesWebEngines: web accepts enabled=true and the closed engine list;
// unknown names and malformed blocklist entries are refused at load (ADR-311/312).
func TestModulesWebEngines(t *testing.T) {
	t.Parallel()
	for _, toml := range []string{
		"[modules.web]\nenabled = true\n",
		"[modules.web]\nenabled = true\nengines = [\"fake\"]\n",
		"[modules.web]\nenabled = false\nengines = [\"fake\", \"brave\", \"tavily\", \"exa\"]\n",
		"[modules.web]\nblocklist = [\"example.com\", \"example.org/private\"]\n",
	} {
		if _, err := parse(t, toml); err != nil {
			t.Errorf("Parse(%q): %v", toml, err)
		}
	}
	tests := []struct{ name, toml, key string }{
		{"unknown", "[modules.web]\nengines = [\"nope\"]\n", "modules.web.engines"},
		{"bad_blocklist", "[modules.web]\nblocklist = [\"https://example.com\"]\n", "modules.web.blocklist"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, tc.toml)
			ke := keyErrorOf(t, err, ErrInvalidValue)
			if ke.Key != tc.key {
				t.Errorf("key = %q, want %q", ke.Key, tc.key)
			}
		})
	}
}
