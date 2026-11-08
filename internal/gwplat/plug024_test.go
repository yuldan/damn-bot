package gwplat_test

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// coreSurfaces are the three gateway surfaces that are not plugin adapters
// (F7-ACCEPTANCE §2 note: PLAT 30/30 = 26 adapter + three core surfaces, and
// PLUG-024 covers the 27 plugin platforms including F4 Telegram).
func coreSurfaces() map[string]bool {
	return map[string]bool{"api_server": true, "msgraph_webhook": true, "webhook": true}
}

// pluginNames is the 27 plugin adapter names derived from the SEC-10 table;
// it never duplicates a list kept elsewhere.
func pluginNames() []string {
	core := coreSurfaces()
	var out []string
	for _, name := range gateway.PlatformNames() {
		if !core[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// TestF7PLUG024PlatformPlugins pins PLUG-024: the 27 plugin adapters are the
// SEC-10 platform table minus the three core surfaces, every one is present in
// the closed registry, default-off, and derived from the static descriptor
// table rather than the environment.
func TestF7PLUG024PlatformPlugins(t *testing.T) {
	plugins := pluginNames()
	if len(plugins) != 27 {
		t.Fatalf("plugin adapters = %d, want 27: %v", len(plugins), plugins)
	}
	// Poisoned environment: a registry that discovered adapters or enabled
	// them from env would change its contents here. Descriptors stay static.
	for _, name := range plugins {
		upper := strings.ToUpper(name)
		t.Setenv("DAMNBOT_"+upper+"_ENABLED", "1")
		t.Setenv("DAMNBOT_"+upper+"_TOKEN", "poison-"+name)
		t.Setenv(upper+"_BOT_TOKEN", "poison-"+name)
	}
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range plugins {
		d, err := reg.Lookup(name)
		if err != nil {
			t.Errorf("plugin %q is not registered: %v", name, err)
			continue
		}
		if !d.DefaultOff {
			t.Errorf("%q is default-on", name)
		}
		want, ok := gateway.CapsFor(name)
		if !ok {
			t.Errorf("%q has no SEC-10 caps", name)
			continue
		}
		if name == "irc" {
			want = gateway.IRCCaps(true)
		}
		if d.Caps != want {
			t.Errorf("%q caps diverge from the SEC-10 table", name)
		}
		if d.Render == "" {
			t.Errorf("%q has no renderer", name)
		}
	}
}

// TestF7PlatformRegistryClosed is the PLUG-024 negative control: the registry
// is exactly the SEC-10 table minus the api_server surface, and alien names,
// duplicates and default-on descriptors are refused.
func TestF7PlatformRegistryClosed(t *testing.T) {
	t.Parallel()
	reg, regErr := gwplat.NewDefaultRegistry()
	if regErr != nil {
		t.Fatal(regErr)
	}
	expected := len(gateway.PlatformNames()) - 1 // all but api_server
	if reg.Len() != expected {
		t.Fatalf("registry = %d descriptors, want %d", reg.Len(), expected)
	}
	for _, name := range gateway.PlatformNames() {
		_, err := reg.Lookup(name)
		if name == "api_server" {
			if !errors.Is(err, gwplat.ErrUnknown) {
				t.Errorf("api_server lookup = %v, want ErrUnknown (not a plugin adapter)", err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q missing from the closed registry: %v", name, err)
		}
	}
	if _, err := reg.Lookup("not-a-platform"); !errors.Is(err, gwplat.ErrUnknown) {
		t.Fatalf("unknown lookup = %v, want ErrUnknown", err)
	}
	seed, err := reg.Lookup("buzz")
	if err != nil {
		t.Fatal(err)
	}
	alien := seed
	alien.Name = "not-a-platform"
	if err := gwplat.NewRegistry().Register(&alien); !errors.Is(err, gwplat.ErrDescriptor) {
		t.Fatalf("alien platform = %v, want ErrDescriptor", err)
	}
	duplicate := seed
	if err := reg.Register(&duplicate); !errors.Is(err, gwplat.ErrDuplicate) {
		t.Fatalf("duplicate register = %v, want ErrDuplicate", err)
	}
	alwaysOn := seed
	alwaysOn.DefaultOff = false
	if err := gwplat.NewRegistry().Register(&alwaysOn); !errors.Is(err, gwplat.ErrDescriptor) {
		t.Fatalf("default-on descriptor = %v, want ErrDescriptor", err)
	}
	if got := pluginNames(); !slices.Contains(got, "telegram") {
		t.Fatalf("plugin list lost telegram: %v", got)
	}
}
