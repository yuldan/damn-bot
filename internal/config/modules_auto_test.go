//go:build !minimal

package config

import (
	"errors"
	"testing"
)

// TestAutoConfigDefaultOff: every [modules.auto.*] section exists with a
// budget but no consent, so the zero config makes no auxiliary call.
func TestAutoConfigDefaultOff(t *testing.T) {
	t.Parallel()
	cfg := Default()
	for _, row := range []struct {
		name string
		val  ModulesAutoFeature
	}{
		{"review", cfg.Modules.Auto.Review},
		{"title", cfg.Modules.Auto.Title},
		{"curator", cfg.Modules.Auto.Curator},
		{"compaction", cfg.Modules.Auto.Compaction},
		{"blueprints", cfg.Modules.Auto.Blueprints},
		{"suggestions", cfg.Modules.Auto.Suggestions},
		{"goals", cfg.Modules.Auto.Goals},
		{"loops", cfg.Modules.Auto.Loops},
		{"heartbeat", cfg.Modules.Auto.Heartbeat},
		{"disk_cleanup", cfg.Modules.Auto.DiskCleanup},
		{"maintenance", cfg.Modules.Auto.Maintenance},
		{"webhooks", cfg.Modules.Auto.Webhooks},
	} {
		if row.val.Enabled {
			t.Errorf("modules.auto.%s is enabled by default", row.name)
		}
		if row.val.DailyTokens != 0 || row.val.DailyUSD != 0 {
			t.Errorf("modules.auto.%s has an implicit budget", row.name)
		}
	}
	if cfg.Modules.Auto.Loops.MinIntervalS != 0 || cfg.Modules.Auto.Heartbeat.MinIntervalS != 0 {
		t.Errorf("implicit loop/heartbeat interval = %d/%d", cfg.Modules.Auto.Loops.MinIntervalS, cfg.Modules.Auto.Heartbeat.MinIntervalS)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAutoConfigEnableAndStrict(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[modules.auto.title]\nenabled = true\ndaily_tokens = 100\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.Modules.Auto.Title.Enabled || cfg.Modules.Auto.Title.DailyTokens != 100 {
		t.Fatalf("title = %+v", cfg.Modules.Auto.Title)
	}
	if cfg.Modules.Auto.Review.Enabled {
		t.Fatal("enabling title enabled review")
	}

	cases := []struct {
		name string
		toml string
		kind error
		key  string
	}{
		{"unknown", "[modules.auto.reviewx]\nenabled = true\n", ErrUnknownKey, "modules.auto.reviewx"},
		{"negative", "[modules.auto.title]\ndaily_tokens = -1\n", ErrInvalidValue, "modules.auto.title.daily_tokens"},
		{"loop_interval", "[modules.auto.loops]\nenabled = true\nmin_interval_s = 10\n", ErrInvalidValue, "modules.auto.loops.min_interval_s"},
		{"unused_interval", "[modules.auto.title]\nmin_interval_s = 5\n", ErrInvalidValue, "modules.auto.title.min_interval_s"},
	}
	for _, tc := range cases {
		_, err := parse(t, tc.toml)
		var ke *Error
		if !errors.As(err, &ke) || !errors.Is(err, tc.kind) || ke.Key != tc.key {
			t.Errorf("%s: err = %v, want %v at %s", tc.name, err, tc.kind, tc.key)
		}
	}
}
