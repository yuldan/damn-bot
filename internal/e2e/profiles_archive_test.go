package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const archiveProfileConfig = `[provider]
model = "openai/gpt-fake"

[ui]
theme = "tern-dark"
`

type archiveManifest struct {
	Schema    int    `json:"schema"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Damnbot   string `json:"damnbot"`
}

func writeNamedProfileConfig(t *testing.T, e *Env, name, content string) {
	t.Helper()
	dir := filepath.Join(e.ConfigDir(), "profiles", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readProfileConfig(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(dir), "config.toml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func profileConfigHash(t *testing.T, dir string) string {
	t.Helper()
	sum := sha256.Sum256(readProfileConfig(t, dir))
	return hex.EncodeToString(sum[:])
}

func archiveEntries(t *testing.T, e *Env, rel string) map[string][]byte {
	t.Helper()
	data := []byte(e.ReadFile(rel))
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := gz.Close(); cerr != nil {
			t.Fatal(cerr)
		}
	}()
	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		var buf bytes.Buffer
		if _, cerr := buf.ReadFrom(tr); cerr != nil {
			t.Fatal(cerr)
		}
		out[hdr.Name] = buf.Bytes()
	}
	return out
}

type testEntry struct {
	name string
	body []byte
	kind byte
	link string
}

func testArchive(t *testing.T, entries ...testEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		hdr := &tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body)), Typeflag: kind, Linkname: entry.link}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) > 0 {
			if _, err := tw.Write(entry.body); err != nil {
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

func testManifest(t *testing.T, schema int) []byte {
	t.Helper()
	b, err := json.Marshal(archiveManifest{Schema: schema, Name: "bad", CreatedAt: "2026-10-28T00:00:00Z", Damnbot: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConfigProfilesExportImportRoundTrip(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeNamedProfileConfig(t, e, "work", archiveProfileConfig)
	e.Exec("config", "profiles", "export", "work", "-o", "work.tar.gz").RequireOK(t)
	entries := archiveEntries(t, e, "work.tar.gz")
	if len(entries) != 2 {
		t.Fatalf("archive entries = %d, want 2: %v", len(entries), entries)
	}
	var manifest archiveManifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != 1 || manifest.Name != "work" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if string(entries["config.toml"]) != archiveProfileConfig {
		t.Fatalf("archive config = %q", entries["config.toml"])
	}
	e.Exec("config", "profiles", "import", "work.tar.gz", "--name", "copy").RequireOK(t)
	got := readProfileConfig(t, filepath.Join(e.ConfigDir(), "profiles", "copy"))
	if string(got) != archiveProfileConfig {
		t.Fatalf("imported config = %q", got)
	}
}

func TestConfigProfilesExportRejectsSecrets(t *testing.T) {
	t.Parallel()
	e := New(t)
	const leaked = "archive-test-value"
	writeNamedProfileConfig(t, e, "secret", "[exec.env]\nTOKEN = \""+leaked+"\"\n")
	res := e.Exec("config", "profiles", "export", "secret", "-o", "secret.tar.gz")
	if res.Code == 0 {
		t.Fatalf("export exit = 0, want non-zero\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "exec.env.TOKEN") {
		t.Errorf("stderr does not name the secret key: %s", res.Stderr)
	}
	if strings.Contains(res.Stderr, leaked) || strings.Contains(res.Stdout, leaked) {
		t.Error("export leaked the secret value")
	}
	if _, err := os.Stat(e.Path("secret.tar.gz")); err == nil {
		t.Error("export wrote an archive despite the secret")
	}
}

func TestConfigProfilesImportRejectsBroken(t *testing.T) {
	t.Parallel()
	e := New(t)
	cases := []struct {
		name string
		data []byte
	}{
		{"garbage", []byte("not a tar.gz")},
		{"schema", testArchive(t,
			testEntry{name: "manifest.json", body: testManifest(t, 2)},
			testEntry{name: "config.toml", body: []byte(archiveProfileConfig)},
		)},
		{"extra", testArchive(t,
			testEntry{name: "manifest.json", body: testManifest(t, 1)},
			testEntry{name: "config.toml", body: []byte(archiveProfileConfig)},
			testEntry{name: "skills/evil.md", body: []byte("x")},
		)},
		{"traversal", testArchive(t,
			testEntry{name: "manifest.json", body: testManifest(t, 1)},
			testEntry{name: "../config.toml", body: []byte(archiveProfileConfig)},
		)},
		{"symlink", testArchive(t,
			testEntry{name: "manifest.json", body: testManifest(t, 1)},
			testEntry{name: "config.toml", kind: tar.TypeSymlink, link: "/etc/passwd"},
		)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.WriteFile("bad.tar.gz", string(tc.data))
			res := e.Exec("config", "profiles", "import", "bad.tar.gz", "--name", "bad")
			if res.Code == 0 {
				t.Fatalf("import exit = 0, want non-zero\n%s", res.Stderr)
			}
			if _, err := os.Stat(filepath.Join(e.ConfigDir(), "profiles", "bad")); err == nil {
				t.Fatal("invalid archive created the target profile")
			}
		})
	}
}

func TestConfigProfilesImportOverwriteNeedsYes(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeNamedProfileConfig(t, e, "work", archiveProfileConfig)
	e.Exec("config", "profiles", "export", "work", "-o", "work.tar.gz").RequireOK(t)
	e.Exec("config", "profiles", "import", "work.tar.gz", "--name", "copy").RequireOK(t)
	target := filepath.Join(e.ConfigDir(), "profiles", "copy")
	if err := os.WriteFile(filepath.Join(target, "config.toml"), []byte("[provider]\nmodel = \"openai/other\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := profileConfigHash(t, target)
	res := e.Exec("config", "profiles", "import", "work.tar.gz", "--name", "copy")
	if res.Code != 2 || !strings.Contains(res.Stderr, "--yes") {
		t.Fatalf("overwrite without --yes = (%d, %s), want exit 2 naming --yes", res.Code, res.Stderr)
	}
	if after := profileConfigHash(t, target); after != before {
		t.Fatal("overwrite without --yes changed the target")
	}
	e.Exec("config", "profiles", "import", "work.tar.gz", "--name", "copy", "--yes").RequireOK(t)
	if got := readProfileConfig(t, target); string(got) != archiveProfileConfig {
		t.Fatalf("overwritten config = %q", got)
	}
}

func TestConfigProfilesImportDryRun(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeNamedProfileConfig(t, e, "work", archiveProfileConfig)
	e.Exec("config", "profiles", "export", "work", "-o", "work.tar.gz").RequireOK(t)
	res := e.Exec("config", "profiles", "import", "work.tar.gz", "--name", "dry", "--dry-run")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "dry run") {
		t.Errorf("dry-run stdout = %q", res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(e.ConfigDir(), "profiles", "dry")); err == nil {
		t.Fatal("dry-run created the target profile")
	}
	e.Exec("config", "profiles", "import", "work.tar.gz", "--name", "copy").RequireOK(t)
	target := filepath.Join(e.ConfigDir(), "profiles", "copy")
	before := profileConfigHash(t, target)
	e.Exec("config", "profiles", "import", "work.tar.gz", "--name", "copy", "--yes", "--dry-run").RequireOK(t)
	if after := profileConfigHash(t, target); after != before {
		t.Fatal("dry-run changed an existing target")
	}
}

func TestConfigModulesEmpty(t *testing.T) {
	t.Parallel()
	e := New(t)
	res := e.Exec("config", "modules")
	res.RequireOK(t)
	if res.Stdout != "no modules registered in this build\n" {
		t.Fatalf("modules stdout = %q", res.Stdout)
	}
	res = e.Exec("config", "modules", "restart", "x")
	if res.Code != 2 || res.Stdout != "" || !strings.Contains(res.Stderr, "serve/restart is F4") {
		t.Fatalf("restart = (%d, %q, %q), want exit 2", res.Code, res.Stdout, res.Stderr)
	}
}
