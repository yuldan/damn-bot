//go:build !minimal

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

// poolManifestVersion is the on-disk shape of the credential-pool manifest.
const poolManifestVersion = 1

// poolRef is one persisted pool member. It names a source, never a value:
// Env is an environment variable and Keychain is a named-secret id.
type poolRef struct {
	ID       string `json:"id"`
	Env      string `json:"env,omitempty"`
	Keychain string `json:"keychain,omitempty"`
}

// poolManifest keeps every pool member ever configured. Load merges the
// current config into it and saves the union, so an entry whose env var or
// keychain item is missing in this process is retained, not deleted
// (EC-PROV-021).
type poolManifest struct {
	Version   int                  `json:"version"`
	Providers map[string][]poolRef `json:"providers"`
}

// loadPoolManifest reads path; a missing file is an empty manifest.
func loadPoolManifest(path string) (poolManifest, error) {
	m := poolManifest{Version: poolManifestVersion, Providers: map[string][]poolRef{}}
	if path == "" {
		return m, nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- path is the run data directory
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, fmt.Errorf("pool manifest: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("pool manifest: %w", err)
	}
	if m.Version != poolManifestVersion {
		return m, fmt.Errorf("pool manifest: unsupported version %d", m.Version)
	}
	if m.Providers == nil {
		m.Providers = map[string][]poolRef{}
	}
	return m, nil
}

// merge reconciles the configured keychain members with the manifest while
// keeping every env member: an env variable absent from this process must not
// delete the row, but a keychain member removed from config is removed.
func (m *poolManifest) merge(provider string, refs []poolRef) {
	keep := make([]poolRef, 0, len(m.Providers[provider])+len(refs))
	for _, existing := range m.Providers[provider] {
		if existing.Keychain == "" { // env-sourced rows are retained
			keep = append(keep, existing)
		}
	}
	for _, ref := range refs {
		updated := false
		for i := range keep {
			if keep[i].ID == ref.ID {
				keep[i] = ref
				updated = true
				break
			}
		}
		if !updated {
			keep = append(keep, ref)
		}
	}
	m.Providers[provider] = keep
}

// savePoolManifest writes the manifest atomically with 0600 permissions.
func savePoolManifest(path string, m poolManifest) error {
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("pool manifest: encode: %w", err)
	}
	dir := filepath.Dir(path)
	if merr := os.MkdirAll(dir, 0o700); merr != nil {
		return fmt.Errorf("pool manifest: mkdir: %w", merr)
	}
	f, err := os.CreateTemp(dir, ".provider-pools-*")
	if err != nil {
		return fmt.Errorf("pool manifest: create: %w", err)
	}
	name := f.Name()
	defer removeTemp(name)
	if err := f.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("pool manifest: chmod: %w", err), f.Close())
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return errors.Join(fmt.Errorf("pool manifest: write: %w", err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("pool manifest: sync: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("pool manifest: close: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("pool manifest: commit: %w", err)
	}
	return nil
}

// providerPoolsAt loads the persisted manifest, folds the configured pools
// into it, saves the union, and builds a pool from every source that resolves
// in this process. Entries that do not resolve stay in the manifest for a
// later process with the environment or keychain available.
func providerPoolsAt(cfg *config.Config, manifestPath string, getenv func(string) string,
	getKeychain func(string) (string, error),
) (map[string]*credpool.Pool, error) {
	m, err := loadPoolManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	if err := mergeConfiguredPools(&m, cfg); err != nil {
		return nil, err
	}
	if len(m.Providers) > 0 {
		if err := savePoolManifest(manifestPath, m); err != nil {
			return nil, err
		}
	}
	return poolsFromManifest(m, getenv, getKeychain)
}

// mergeConfiguredPools folds the current config into the manifest without
// removing stored env entries.
func mergeConfiguredPools(m *poolManifest, cfg *config.Config) error {
	for i := range cfg.Providers.Custom {
		c := &cfg.Providers.Custom[i]
		refs := make([]poolRef, 0, len(c.Credentials))
		for _, pc := range c.Credentials {
			id, ok := strings.CutPrefix(pc.Secret, "keychain:")
			if !ok || id == "" {
				return fmt.Errorf("providers.custom[%q].credentials[%q]: must be a keychain:<id> reference", c.Name, pc.ID)
			}
			refs = append(refs, poolRef{ID: pc.ID, Keychain: id})
		}
		m.merge(c.Name, refs)
	}
	return nil
}

// poolsFromManifest builds a pool from every member that resolves now;
// unresolved env members stay in the manifest.
func poolsFromManifest(m poolManifest, getenv func(string) string,
	getKeychain func(string) (string, error),
) (map[string]*credpool.Pool, error) {
	pools := make(map[string]*credpool.Pool, len(m.Providers))
	for name, refs := range m.Providers {
		creds := make([]credpool.Credential, 0, len(refs))
		for _, ref := range refs {
			cred, ok, err := resolvePoolRef(name, ref, getenv, getKeychain)
			if err != nil {
				return nil, err
			}
			if ok {
				creds = append(creds, cred)
			}
		}
		if len(creds) == 0 {
			continue
		}
		pool, err := credpool.New(creds)
		if err != nil {
			return nil, fmt.Errorf("providers.custom[%q]: %w", name, err)
		}
		pools[name] = pool
	}
	return pools, nil
}

// resolvePoolRef returns the credential value of one manifest member. A
// missing env value is not an error (the manifest keeps the row); a missing
// configured keychain value stays fail-closed.
func resolvePoolRef(name string, ref poolRef, getenv func(string) string,
	getKeychain func(string) (string, error),
) (credpool.Credential, bool, error) {
	switch {
	case ref.Keychain != "":
		value, err := getKeychain(ref.Keychain)
		if err != nil {
			return credpool.Credential{}, false, fmt.Errorf("providers.custom[%q].credentials[%q]: %w", name, ref.ID, err)
		}
		return credpool.NewCredential(ref.ID, value), value != "", nil
	case ref.Env != "":
		value := getenv(ref.Env)
		return credpool.NewCredential(ref.ID, value), value != "", nil
	default:
		return credpool.Credential{}, false, nil
	}
}

// removeTemp deletes a scratch file; a missing file is already clean.
func removeTemp(name string) {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
}

// poolManifestAt returns the manifest path below the run data directory.
func poolManifestAt(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "provider-pools.json")
}
