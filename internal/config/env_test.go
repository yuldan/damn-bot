package config

import (
	"errors"
	"testing"
	"time"
)

func envLoader(t *testing.T, env map[string]string) *Loader {
	t.Helper()
	l, err := New(Deps{
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("no file") },
		Getenv:   func(k string) string { return env[k] },
		HomeDir:  func() (string, error) { return "/home/u", nil },
		GOOS:     "linux",
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestEnvOverrides (GAP-OPS-01): the generated DAMNBOT_<SECTION>_<KEY> name
// overrides bool, int, string and duration fields from the schema.
func TestEnvOverrides(t *testing.T) {
	l := envLoader(t, map[string]string{
		"DAMNBOT_NETWORK_BACKGROUND":      "false",
		"DAMNBOT_LOOP_MAX_ITERATIONS":     "7",
		"DAMNBOT_PROVIDER_MODEL":          "openai/gpt-test",
		"DAMNBOT_LOG_MESSAGE_CONTENT_TTL": "90s",
	})
	cfg := Default()
	if err := l.Effective(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Network.Background {
		t.Error("bool override")
	}
	if cfg.Loop.MaxIterations != 7 {
		t.Errorf("int override = %d", cfg.Loop.MaxIterations)
	}
	if cfg.Provider.Model != "openai/gpt-test" {
		t.Errorf("string override = %q", cfg.Provider.Model)
	}
	if time.Duration(cfg.Log.MessageContentTTL) != 90*time.Second {
		t.Errorf("duration override = %v", cfg.Log.MessageContentTTL)
	}
}

// TestEnvOverrideInvalid names the variable and fails closed.
func TestEnvOverrideInvalid(t *testing.T) {
	l := envLoader(t, map[string]string{"DAMNBOT_LOOP_MAX_ITERATIONS": "many"})
	cfg := Default()
	err := l.Effective(&cfg)
	var ce *Error
	if !errors.As(err, &ce) || !errors.Is(err, ErrInvalidValue) || ce.Key != "loop.max_iterations" {
		t.Fatalf("err = %v", err)
	}
}

// TestEnvNameHasNoAliases pins the one generated name per key.
func TestEnvNameHasNoAliases(t *testing.T) {
	if got := EnvName("network", "background"); got != "DAMNBOT_NETWORK_BACKGROUND" {
		t.Fatalf("EnvName = %q", got)
	}
}
