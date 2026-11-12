package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	data := filepath.Join(dataRoot, "damnbot")
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
