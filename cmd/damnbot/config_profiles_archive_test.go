package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cleanProfileConfig = `[provider]
model = "openai/gpt-test"

[ui]
theme = "tern-dark"
`

func writeNamedProfile(t *testing.T, cfgDir, name, content string) {
	t.Helper()
	dir := filepath.Join(cfgDir, "profiles", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type rawEntry struct {
	name string
	body []byte
	kind byte
	link string
}

func rawArchive(t *testing.T, entries ...rawEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: kind, Linkname: e.link}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(e.body) > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func manifestJSON(t *testing.T) []byte {
	t.Helper()
	out, err := json.Marshal(profileManifest{Schema: 1, Name: "bad", CreatedAt: "2026-10-28T00:00:00Z", Damnbot: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProfileArchiveRoundTrip(t *testing.T) {
	_, cfgDir := configTestHome(t, cleanProfileConfig)
	writeNamedProfile(t, cfgDir, "work", cleanProfileConfig)
	out := filepath.Join(t.TempDir(), "work.tar.gz")
	code, _, stderr := runArgs(t, "config", "profiles", "export", "work", "-o", out)
	if code != 0 {
		t.Fatalf("export exit = %d: %s", code, stderr)
	}
	manifest, cfg, err := readProfileArchive(readTestFile(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != profileArchiveSchema || manifest.Name != "work" || manifest.CreatedAt == "" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if string(cfg) != cleanProfileConfig {
		t.Fatalf("archive config = %q", cfg)
	}
	code, _, stderr = runArgs(t, "config", "profiles", "import", out, "--name", "copy")
	if code != 0 {
		t.Fatalf("import exit = %d: %s", code, stderr)
	}
	got := readTestFile(t, filepath.Join(cfgDir, "profiles", "copy", "config.toml"))
	if string(got) != cleanProfileConfig {
		t.Fatalf("imported config = %q", got)
	}
	code, _, stderr = runArgs(t, "config", "profiles", "export", "work", "-o", filepath.Join(cfgDir, "profiles", "work", "config.toml"))
	if code == 0 || !strings.Contains(stderr, "must not be the profile config") {
		t.Fatalf("export over config = (%d, %s), want refusal", code, stderr)
	}
}

func TestProfileArchiveExportRejectsSecrets(t *testing.T) {
	_, cfgDir := configTestHome(t, cleanProfileConfig)
	const leaked = "archive-test-value"
	writeNamedProfile(t, cfgDir, "secret", "[exec.env]\nTOKEN = \""+leaked+"\"\n")
	out := filepath.Join(t.TempDir(), "secret.tar.gz")
	code, stdout, stderr := runArgs(t, "config", "profiles", "export", "secret", "-o", out)
	if code != 1 {
		t.Fatalf("export exit = %d, want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "exec.env.TOKEN") {
		t.Errorf("stderr does not name the secret key: %s", stderr)
	}
	if strings.Contains(stderr, leaked) || strings.Contains(stdout, leaked) {
		t.Error("export leaked the secret value")
	}
	if _, err := os.Stat(out); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("archive was written despite the secret: %v", err)
	}
}

func TestSecretConfigKeys(t *testing.T) {
	data := []byte(`[exec]
env = { TOKEN = "supersecret" }

[[providers.custom]]
name = "kantor"
base_url = "http://10.0.0.5:8000/v1"
api_key = "keychain:kantor"
extra_headers = { Authorization = "Bearer topsecret" }
`)
	keys, err := secretConfigKeys(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec.env.TOKEN", "providers.custom.extra_headers.Authorization"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("secret keys = %v, want %v", keys, want)
	}
	refs, err := secretConfigKeys([]byte("[providers.custom]\napi_key = \"keychain:x\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("keychain references flagged as secrets: %v", refs)
	}
}

func TestProfileArchiveImportRejectsBroken(t *testing.T) {
	_, cfgDir := configTestHome(t, cleanProfileConfig)
	cases := []struct {
		name string
		data []byte
	}{
		{"garbage", []byte("not a tar.gz")},
		{"schema", mustArchive(t, profileManifest{Schema: 2, Name: "bad"}, []byte(cleanProfileConfig))},
		{"extra", rawArchive(t,
			rawEntry{name: profileManifestEntry, body: manifestJSON(t)},
			rawEntry{name: profileConfigEntry, body: []byte(cleanProfileConfig)},
			rawEntry{name: "skills/evil.md", body: []byte("x")},
		)},
		{"traversal", rawArchive(t,
			rawEntry{name: profileManifestEntry, body: manifestJSON(t)},
			rawEntry{name: "../config.toml", body: []byte(cleanProfileConfig)},
		)},
		{"symlink", rawArchive(t,
			rawEntry{name: profileManifestEntry, body: manifestJSON(t)},
			rawEntry{name: profileConfigEntry, kind: tar.TypeSymlink, link: "/etc/passwd"},
		)},
		{"duplicate", rawArchive(t,
			rawEntry{name: profileManifestEntry, body: manifestJSON(t)},
			rawEntry{name: profileConfigEntry, body: []byte(cleanProfileConfig)},
			rawEntry{name: profileConfigEntry, body: []byte(cleanProfileConfig)},
		)},
		{"missing-config", rawArchive(t,
			rawEntry{name: profileManifestEntry, body: manifestJSON(t)},
		)},
		{"invalid-config", rawArchive(t,
			rawEntry{name: profileManifestEntry, body: manifestJSON(t)},
			rawEntry{name: profileConfigEntry, body: []byte("[loop]\nmax_iterations = 99\n")},
		)},
		{"secret-config", rawArchive(t,
			rawEntry{name: profileManifestEntry, body: manifestJSON(t)},
			rawEntry{name: profileConfigEntry, body: []byte("[exec.env]\nTOKEN = \"archive-test-value\"\n")},
		)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.tar.gz")
			writeBytes(t, path, tc.data)
			code, _, stderr := runArgs(t, "config", "profiles", "import", path, "--name", "bad")
			if code == 0 {
				t.Fatalf("import exit = 0, want non-zero\n%s", stderr)
			}
			if _, err := os.Stat(filepath.Join(cfgDir, "profiles", "bad")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("import wrote the target profile despite invalid archive: %v", err)
			}
		})
	}
}

func TestProfileArchiveImportOverwriteAndDryRun(t *testing.T) {
	_, cfgDir := configTestHome(t, cleanProfileConfig)
	writeNamedProfile(t, cfgDir, "work", cleanProfileConfig)
	out := filepath.Join(t.TempDir(), "work.tar.gz")
	if code, _, stderr := runArgs(t, "config", "profiles", "export", "work", "-o", out); code != 0 {
		t.Fatalf("export = %d: %s", code, stderr)
	}
	if code, _, stderr := runArgs(t, "config", "profiles", "import", out, "--name", "copy"); code != 0 {
		t.Fatalf("first import = %d: %s", code, stderr)
	}
	target := filepath.Join(cfgDir, "profiles", "copy", "config.toml")
	writeBytes(t, target, []byte("[provider]\nmodel = \"openai/other\"\n"))
	before := configTestHash(t, target)
	code, _, stderr := runArgs(t, "config", "profiles", "import", out, "--name", "copy")
	if code != 2 || !strings.Contains(stderr, "--yes") {
		t.Fatalf("overwrite without --yes = (%d, %s), want exit 2 naming --yes", code, stderr)
	}
	if after := configTestHash(t, target); after != before {
		t.Fatal("overwrite without --yes changed the target")
	}
	if code, _, stderr := runArgs(t, "config", "profiles", "import", out, "--name", "copy", "--dry-run", "--yes"); code != 0 {
		t.Fatalf("dry-run = %d: %s", code, stderr)
	}
	if after := configTestHash(t, target); after != before {
		t.Fatal("dry-run changed the target")
	}
	if code, _, stderr := runArgs(t, "config", "profiles", "import", out, "--name", "copy", "--yes"); code != 0 {
		t.Fatalf("overwrite = %d: %s", code, stderr)
	}
	if got := readTestFile(t, target); string(got) != cleanProfileConfig {
		t.Fatalf("overwritten config = %q", got)
	}
	dryName := "dry"
	if code, _, stderr := runArgs(t, "config", "profiles", "import", out, "--name", dryName, "--dry-run"); code != 0 {
		t.Fatalf("new dry-run = %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "profiles", dryName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry-run created the target profile: %v", err)
	}
}

func TestConfigModulesEmpty(t *testing.T) {
	configTestHome(t, cleanProfileConfig)
	code, out, stderr := runArgs(t, "config", "modules")
	if code != 0 || out != "no modules registered in this build\n" {
		t.Fatalf("modules = (%d, %q, %s)", code, out, stderr)
	}
	code, out, stderr = runArgs(t, "config", "modules", "restart", "x")
	if code != 2 || out != "" || !strings.Contains(stderr, "serve/restart is F4") {
		t.Fatalf("restart = (%d, %q, %s), want exit 2", code, out, stderr)
	}
}

func TestModuleEnabledMap(t *testing.T) {
	configTestHome(t, "[modules.gateway.telegram]\nenabled = true\n\n[modules.browser]\nenabled = false\n")
	got, err := moduleEnabledMap()
	if err != nil {
		t.Fatal(err)
	}
	if !got["gateway.telegram"] || got["browser"] {
		t.Fatalf("enabled map = %v", got)
	}
}

func mustArchive(t *testing.T, manifest profileManifest, cfg []byte) []byte {
	t.Helper()
	data, err := buildProfileArchive(manifest, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
