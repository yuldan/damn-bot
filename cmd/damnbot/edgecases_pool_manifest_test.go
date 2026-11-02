//go:build !minimal

package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/credpool"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// envKeyRef returns the stored entry ids of one provider pool.
func envKeyRef(pool *credpool.Pool) []string {
	var ids []string
	for range pool.Len() {
		c, ok := pool.Acquire(time.Now())
		if !ok {
			break
		}
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestPoolManifestLoadIsNonDestructive (EC-PROV-021): loading a pool in a
// process where an env-sourced member is missing must not delete that member
// from disk. The configured keychain member is still usable, and the env
// member joins the pool in a later process that has the variable.
func TestPoolManifestLoadIsNonDestructive(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-021", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-021", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "provider-pools.json")
		stored := `{"version":1,"providers":{"legacy":[{"id":"from-env","env":"LEGACY_KEY"}]}}`
		if err := os.WriteFile(path, []byte(stored), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cfg.Providers.Custom = []config.CustomProvider{{
			Name: "legacy", BaseURL: "http://127.0.0.1:1/v1", API: "openai-chat",
			Models:      []config.CustomModel{{ID: "m"}},
			Credentials: []config.ProviderCredential{{ID: "configured", Secret: "keychain:cfg"}},
		}}
		keychain := func(string) (string, error) { return "sk-configured", nil }
		pools, err := providerPoolsAt(&cfg, path, func(string) string { return "" }, keychain)
		if err != nil {
			t.Fatalf("first load: %v", err)
		}
		if got := envKeyRef(pools["legacy"]); !slices.Equal(got, []string{"configured"}) {
			t.Fatalf("first load ids = %v, want [configured]", got)
		}
		m, err := loadPoolManifest(path)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, ref := range m.Providers["legacy"] {
			ids = append(ids, ref.ID)
		}
		if !slices.Contains(ids, "from-env") || !slices.Contains(ids, "configured") {
			t.Fatalf("manifest lost an entry: %v", ids)
		}
		// A later process with the env var gets both members; nothing was
		// rewritten away in between.
		pools, err = providerPoolsAt(&cfg, path, func(string) string { return "sk-env" }, keychain)
		if err != nil {
			t.Fatalf("second load: %v", err)
		}
		if got := envKeyRef(pools["legacy"]); !slices.Equal(got, []string{"configured", "from-env"}) {
			t.Fatalf("second load ids = %v", got)
		}
	})
}

func TestPoolManifestMissingKeychainFailsClosed(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-021", "F5", "covered") {
		return
	}
	path := filepath.Join(t.TempDir(), "provider-pools.json")
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "legacy", BaseURL: "http://127.0.0.1:1/v1", API: "openai-chat",
		Models:      []config.CustomModel{{ID: "m"}},
		Credentials: []config.ProviderCredential{{ID: "configured", Secret: "keychain:cfg"}},
	}}
	if _, err := providerPoolsAt(&cfg, path, func(string) string { return "" },
		func(string) (string, error) { return "", errors.New("missing") }); err == nil {
		t.Fatal("missing configured keychain was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest not retained after fail-closed: %v", err)
	}
}

// TestPoolManifestKeychainFollowsConfig: only env rows are retained across a
// missing env; a keychain member deleted from config is removed instead of
// lingering in the pool.
func TestPoolManifestKeychainFollowsConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "provider-pools.json")
	cfg := config.Default()
	cfg.Providers.Custom = []config.CustomProvider{{
		Name: "legacy", BaseURL: "http://127.0.0.1:1/v1", API: "openai-chat",
		Models:      []config.CustomModel{{ID: "m"}},
		Credentials: []config.ProviderCredential{{ID: "configured", Secret: "keychain:cfg"}},
	}}
	keychain := func(string) (string, error) { return "sk-configured", nil }
	if _, err := providerPoolsAt(&cfg, path, func(string) string { return "" }, keychain); err != nil {
		t.Fatal(err)
	}
	cfg.Providers.Custom[0].Credentials = nil
	pools, err := providerPoolsAt(&cfg, path, func(string) string { return "" }, keychain)
	if err != nil {
		t.Fatal(err)
	}
	if pools["legacy"] != nil {
		t.Fatalf("removed keychain member still built a pool: %v", envKeyRef(pools["legacy"]))
	}
}
