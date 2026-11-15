package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/setup"
)

func testSHA(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestSetupUninstallCLI(t *testing.T) {
	home := t.TempDir()
	dataRoot := filepath.Join(home, "data")
	cfgRoot := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", dataRoot)
	t.Setenv("APPDATA", cfgRoot)
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)
	loader, lerr := config.New(config.OS())
	if lerr != nil {
		t.Fatal(lerr)
	}
	paths, perr := loader.Paths("")
	if perr != nil {
		t.Fatal(perr)
	}
	data := paths.DataDir
	bin := filepath.Join(home, "bin", "damnbot")
	rc := filepath.Join(home, ".bashrc")
	for path, body := range map[string]string{bin: "binary", rc: "# user\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rcBlock := "# >>> damnbot >>>\nexport PATH=\"$HOME/bin:$PATH\"\n# <<< damnbot <<<\n"
	if err := os.WriteFile(rc, []byte("# user\n"+rcBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &setup.Manifest{
		Schema: 1, Version: "1.2.3", StateDir: data, InstalledAt: "now",
		Files: []setup.File{
			{Path: bin, Kind: "binary", SHA256After: testSHA(t, bin)},
			{Path: rc, Kind: "rc", SHA256After: testSHA(t, rc)},
		},
		Blocks: []setup.Block{{Path: rc, Begin: setup.BeginMarker, End: setup.EndMarker}},
	}
	if err := setup.WriteManifest(filepath.Join(data, setup.ManifestName), m); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runArgs(t, "setup", "--uninstall")
	if code != 0 {
		t.Fatalf("setup --uninstall = %d %q", code, errOut)
	}
	if !strings.Contains(out, "uninstall:") {
		t.Fatalf("output = %q", out)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Fatalf("binary kept: %v", err)
	}
	got, err := os.ReadFile(rc) // #nosec G304 -- test temp rc path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# user\n" {
		t.Fatalf("rc = %q", got)
	}
}

func TestSetupUninstallDryRunCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	code, _, errOut := runArgs(t, "setup", "--uninstall")
	if code != 1 || !strings.Contains(errOut, "install manifest") {
		t.Fatalf("missing manifest = %d %q", code, errOut)
	}
	if code, _, _ := runArgs(t, "setup", "--uninstall", "--dry-run", "extra"); code != 2 {
		t.Fatalf("extra arg = %d", code)
	}
}

// TestF10UninstallDryRunKeepsChangedFile pins CLI-064/OPS-005 on the existing
// manifest-driven surface: the dry run prints the rc removal as a unified diff
// without touching the file, and a post-install edit is kept rather than
// deleted. There is no second uninstall implementation (ADR-216).
func TestF10UninstallDryRunKeepsChangedFile(t *testing.T) {
	dataDir, bin, rc, block := stageUninstallFixture(t)

	code, out, errOut := runArgs(t, "setup", "--uninstall", "--dry-run")
	if code != 0 {
		t.Fatalf("dry run = %d %q", code, errOut)
	}
	if !strings.Contains(out, "--- "+rc) || !strings.Contains(out, "-"+setup.BeginMarker) {
		t.Fatalf("dry run diff missing: %q", out)
	}
	if !strings.Contains(out, "kept "+bin+" (content changed)") {
		t.Fatalf("dry run did not keep the edited binary: %q", out)
	}
	if got, rerr := os.ReadFile(rc); rerr != nil || string(got) != "# user\n"+block { // #nosec G304 -- test temp rc path
		t.Fatalf("dry run wrote rc: %v %q", rerr, got)
	}

	code, out, errOut = runArgs(t, "setup", "--uninstall")
	if code != 0 {
		t.Fatalf("uninstall = %d %q", code, errOut)
	}
	if !strings.Contains(out, "kept "+bin) {
		t.Fatalf("uninstall output = %q", out)
	}
	if got, rerr := os.ReadFile(bin); rerr != nil || string(got) != "edited after install" { // #nosec G304 -- test temp binary path
		t.Fatalf("edited binary = %v %q", rerr, got)
	}
	if got, rerr := os.ReadFile(rc); rerr != nil || string(got) != "# user\n" { // #nosec G304 -- test temp rc path
		t.Fatalf("rc = %v %q", rerr, got)
	}
	if _, serr := os.Stat(filepath.Join(dataDir, setup.ManifestName)); !os.IsNotExist(serr) {
		t.Fatalf("manifest kept: %v", serr)
	}
}

// stageUninstallFixture lays down an installed binary, an rc block and a
// manifest under a temporary HOME, then edits the binary so the uninstall must
// keep it. It returns the data dir plus the rc path and block for assertions.
func stageUninstallFixture(t *testing.T) (dataDir, bin, rc, block string) {
	t.Helper()
	home := t.TempDir()
	dataRoot := filepath.Join(home, "data")
	cfgRoot := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", dataRoot)
	t.Setenv("APPDATA", cfgRoot)
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)
	loader, err := config.New(config.OS())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(home, "bin", "damnbot")
	rc = filepath.Join(home, ".bashrc")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("installed binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	block = "# >>> damnbot >>>\nexport PATH=\"$HOME/bin:$PATH\"\n# <<< damnbot <<<\n"
	if err := os.WriteFile(rc, []byte("# user\n"+block), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &setup.Manifest{
		Schema: 1, Version: "1.2.3", StateDir: paths.DataDir, InstalledAt: "now",
		Files: []setup.File{
			{Path: bin, Kind: "binary", SHA256After: testSHA(t, bin)},
			{Path: rc, Kind: "rc", SHA256After: testSHA(t, rc)},
		},
		Blocks: []setup.Block{{Path: rc, Begin: setup.BeginMarker, End: setup.EndMarker}},
	}
	if err := setup.WriteManifest(filepath.Join(paths.DataDir, setup.ManifestName), m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("edited after install"), 0o600); err != nil {
		t.Fatal(err)
	}
	return paths.DataDir, bin, rc, block
}
