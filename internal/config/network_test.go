package config

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func networkLoader(t *testing.T, env, data string) *Loader {
	t.Helper()
	l, err := New(Deps{
		ReadFile: func(name string) ([]byte, error) {
			if name == "config.toml" {
				return []byte(data), nil
			}
			return nil, fs.ErrNotExist
		},
		Getenv: func(key string) string {
			if key == "DAMNBOT_NETWORK_BACKGROUND" {
				return env
			}
			return ""
		},
		HomeDir: func() (string, error) { return "/home/u", nil },
		GOOS:    "linux",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

// TestNetworkDefaults: the privacy-safe default enables background egress
// (GAP-OPS-02) and leaves offline mode off (CROSS-CUTTING §4).
func TestNetworkDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Network.Offline || !cfg.Network.Background {
		t.Fatalf("network = %+v, want offline=false background=true", cfg.Network)
	}
}

// TestNetworkKeys: both [network] keys decode from the file.
func TestNetworkKeys(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[network]\noffline = true\nbackground = false\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.Network.Offline || cfg.Network.Background {
		t.Fatalf("network = %+v, want offline=true background=false", cfg.Network)
	}
}

// TestNetworkBackgroundEnv: Effective applies DAMNBOT_NETWORK_BACKGROUND
// while Load and Parse stay file-only, so a stale variable never beats the
// file (EC-CFG-001) and `config edit` validates the file alone (GAP-OPS-02).
func TestNetworkBackgroundEnv(t *testing.T) {
	t.Parallel()
	const file = "[network]\nbackground = true\n"
	l := networkLoader(t, "0", file)
	parsed, err := l.Parse([]byte(file), "config.toml")
	if err != nil || !parsed.Network.Background {
		t.Fatalf("Parse = %+v, %v; want file value true", parsed.Network, err)
	}
	loaded, err := l.Load("config.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Network.Background {
		t.Fatalf("Load background = false, want the file value; env is Effective's input")
	}
	effective := loaded
	if eerr := l.Effective(&effective); eerr != nil {
		t.Fatalf("Effective: %v", eerr)
	}
	if effective.Network.Background {
		t.Fatalf("Effective background = true, want false from env")
	}

	on := networkLoader(t, "true", "[network]\nbackground = false\n")
	loaded, err = on.Load("config.toml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	effective = loaded
	if eerr := on.Effective(&effective); eerr != nil {
		t.Fatalf("Effective: %v", eerr)
	}
	if !effective.Network.Background {
		t.Fatalf("Effective background = false, want env true to win")
	}
}

// TestNetworkInvalid: a bad file value and a bad environment value both name
// network.background and fail closed.
func TestNetworkInvalid(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[network]\nbackground = \"off\"\n")
	var ce *Error
	if !errors.As(err, &ce) || !errors.Is(err, ErrInvalidValue) || ce.Key != "network.background" || !strings.Contains(ce.Msg, "bool") {
		t.Fatalf("file error = %v, want a typed network.background type error", err)
	}
	cfg := Default()
	err = networkLoader(t, "maybe", "").Effective(&cfg)
	if !errors.As(err, &ce) || !errors.Is(err, ErrInvalidValue) || ce.Key != "network.background" {
		t.Fatalf("env error = %v, want ErrInvalidValue on network.background", err)
	}
	if !strings.Contains(err.Error(), "DAMNBOT_NETWORK_BACKGROUND") {
		t.Errorf("env error does not name the variable: %v", err)
	}
}
