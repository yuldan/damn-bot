package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestConfigVersionMarker (EC-CFG-005): config_version declares the schema a
// file expects, and a file newer than the binary is refused with both versions
// in the error instead of being parsed with silent defaults (instalasi
// campuran/basi; EDGE-CASES-hermes.md:248, SPEC §12, ADR-221).
//
// A file without the key is legacy and keeps the default: every config
// written before the marker existed must keep working. The marker is checked
// before the section validators, so the newer file never falls through to a
// half-parsed config.
func TestConfigVersionMarker(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-CFG-005", "F3", "covered") {
		return
	}
	t.Run("EC-CFG-005", func(t *testing.T) {
		t.Parallel()
		t.Run("legacy", func(t *testing.T) {
			t.Parallel()
			configVersionLegacy(t)
		})
		t.Run("newer", func(t *testing.T) {
			t.Parallel()
			configVersionRejects(t)
		})
		t.Run("current", func(t *testing.T) {
			t.Parallel()
			configVersionCurrent(t)
		})
	})
}

// configVersionLegacy covers (a): no marker and an explicit 0 both mean the
// legacy file and keep working.
func configVersionLegacy(t *testing.T) {
	t.Helper()
	legacy, err := parse(t, "[provider]\nmodel = \"openai/gpt-test\"\n")
	if err != nil {
		t.Fatalf("legacy config rejected: %v", err)
	}
	if legacy.ConfigVersion != SchemaVersion || legacy.Provider.Model != "openai/gpt-test" {
		t.Fatalf("legacy = version %d model %q, want %d and the file value", legacy.ConfigVersion, legacy.Provider.Model, SchemaVersion)
	}
	explicitZero, err := parse(t, "config_version = 0\n[provider]\nmodel = \"openai/gpt-test\"\n")
	if err != nil || explicitZero.ConfigVersion != 0 {
		t.Fatalf("config_version = 0: %d, %v", explicitZero.ConfigVersion, err)
	}
}

// configVersionRejects covers (b): a marker outside the supported range fails
// closed, naming the file value and the range; no parsed config leaks out.
func configVersionRejects(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name, body, wantVersion string
	}{
		{"next version", "config_version = 2\n", "2"},
		{"far future", "config_version = 99\n", "99"},
		{"negative", "config_version = -1\n", "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parse(t, tc.body+"[provider]\nmodel = \"openai/gpt-test\"\n")
			if !errors.Is(err, ErrVersion) {
				t.Fatalf("parse = %v, want ErrVersion", err)
			}
			for _, want := range []string{tc.wantVersion, "0..1"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if cfg.Provider.Model != "" {
				t.Fatalf("rejected config leaked parsed values: %+v", cfg.Provider)
			}
		})
	}
}

// configVersionCurrent covers (c) plus the control: the same body parses at a
// supported marker, so the rejection above is the version gate, not broken
// TOML.
func configVersionCurrent(t *testing.T) {
	t.Helper()
	current, err := parse(t, "config_version = 1\n[loop]\nmax_iterations = 7\n")
	if err != nil {
		t.Fatalf("current config rejected: %v", err)
	}
	if current.ConfigVersion != SchemaVersion || current.Loop.MaxIterations != 7 {
		t.Fatalf("current = version %d max_iterations %d", current.ConfigVersion, current.Loop.MaxIterations)
	}
	if _, err := parse(t, "config_version = 0\n[provider]\nmodel = \"openai/gpt-test\"\n"); err != nil {
		t.Fatalf("same body at a supported version: %v", err)
	}
}
