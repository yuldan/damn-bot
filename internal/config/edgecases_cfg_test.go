package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestConfigStaleEnvDoesNotOverrideFile (EC-CFG-001): a stale .env left next
// to the config, and stale variables already in the process environment, do
// not beat config.toml. hermes loaded .env into os.environ and its config
// reader deferred to an existing environment variable ("not in os.environ"),
// so an old .env silently won over the edited config (EDGE-CASES-hermes.md:244).
//
// The F3 loader has one documented source per profile: the TOML file, over
// the struct defaults (SPEC.md:2416, ADR-214 butir 3). Environment overrides
// (`DAMNBOT_<SECTION>_<KEY>`) are still future GAP-OPS-01 work, and a plain
// `.env` is never a config source, so neither becomes a third source here.
func TestConfigStaleEnvDoesNotOverrideFile(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-CFG-001", "F3", "covered") {
		return
	}
	t.Run("EC-CFG-001", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		writeCfgFile(t, cfgPath, "[provider]\nmodel = \"from-file\"\n")
		// A stale .env with the same settings at their old values. It is a
		// plain file next to the config, not a config source.
		writeCfgFile(t, filepath.Join(dir, ".env"), "PROVIDER_MODEL=from-dotenv\nDAMNBOT_PROVIDER_MODEL=from-dotenv\n")
		// The environment a dotenv loader would have produced before the
		// config was edited.
		env := map[string]string{
			"PROVIDER_MODEL":              "from-dotenv",
			"DAMNBOT_PROVIDER_MODEL":      "from-dotenv",
			"DAMNBOT_LOOP_MAX_ITERATIONS": "7",
		}
		var reads, gets []string
		files := os.DirFS(dir)
		l, err := New(Deps{
			ReadFile: func(name string) ([]byte, error) {
				reads = append(reads, name)
				return fs.ReadFile(files, filepath.Base(name))
			},
			Getenv: func(key string) string {
				gets = append(gets, key)
				return env[key]
			},
			HomeDir: func() (string, error) { return dir, nil },
			GOOS:    "linux",
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		cfg, err := l.Load(cfgPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.Provider.Model; got != "from-file" {
			t.Fatalf("provider.model = %q, want the config.toml value", got)
		}
		// An absent key falls to its default, not to the stale environment.
		if got := cfg.Loop.MaxIterations; got != 40 {
			t.Fatalf("loop.max_iterations = %d, want the default 40", got)
		}
		// The loader never consults the environment while reading config and
		// reads only the named file: .env is not a second source.
		if len(gets) != 0 {
			t.Fatalf("Load read env %q, want no environment reads", gets)
		}
		if want := []string{cfgPath}; !slices.Equal(reads, want) {
			t.Fatalf("Load read %q, want only %q", reads, want)
		}

		// Negative control: the same recorder is live for the path
		// convention, which is the only consumer of the environment here. If
		// Load had consulted it, `gets` above would not be empty.
		if _, err := l.Paths(""); err != nil {
			t.Fatalf("Paths: %v", err)
		}
		if len(gets) == 0 {
			t.Fatal("Paths read no environment: the recorder is dead, so the Load assertion proves nothing")
		}
	})
}

func writeCfgFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}
