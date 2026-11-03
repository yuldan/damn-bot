//go:build !minimal

package browser

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func TestEngineSelection(t *testing.T) {
	t.Parallel()
	base := ModuleConfig{Enabled: true, AllowOrigins: []string{"https://app.example"}}
	for _, tc := range []struct {
		name string
		cfg  ModuleConfig
		want string
		err  bool
	}{
		{"none", base, "", false},
		{"cdp", func() ModuleConfig { c := base; c.CDPURL = "ws://127.0.0.1:9222/devtools/page/x"; return c }(), "cdp", false},
		{"explicit cdp", func() ModuleConfig {
			c := base
			c.Engine = "cdp"
			c.CDPURL = "ws://127.0.0.1:9222/devtools/page/x"
			return c
		}(), "cdp", false},
		{"cloud selects", func() ModuleConfig { c := base; c.Engine = "cloud"; return c }(), "cloud", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.cfg
			m, err := New(&cfg, Deps{})
			if tc.err {
				if !errors.Is(err, errBadConfig) {
					t.Fatalf("New = %v, want errBadConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := m.engineName(); got != tc.want {
				t.Fatalf("engine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewRejectsEngineConfig(t *testing.T) {
	t.Parallel()
	base := ModuleConfig{AllowOrigins: []string{"https://app.example"}}
	for _, tc := range []struct {
		name string
		edit func(*ModuleConfig)
	}{
		{"unknown engine", func(c *ModuleConfig) { c.Engine = "webkit" }},
		{"bad cdp", func(c *ModuleConfig) { c.Engine = "cdp"; c.CDPURL = "ws://public.example/x" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tc.edit(&cfg)
			if _, err := New(&cfg, Deps{}); !errors.Is(err, errBadConfig) {
				t.Fatalf("New(%+v) = %v, want errBadConfig", cfg, err)
			}
		})
	}
}

func TestEngineWithoutEndpointFailsClosed(t *testing.T) {
	t.Parallel()
	cfg := moduleConfig()
	cfg.CDPURL = ""
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(newFake())})
	_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v, want module_unavailable", err)
	}
}
