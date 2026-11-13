package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallerManifestUninstall(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".local", "share", "damnbot")
	cfg := filepath.Join(home, ".config", "damnbot")
	bin := filepath.Join(home, "bin", "damnbot")
	rc := filepath.Join(home, ".bashrc")
	mustWrite(t, bin, "binary", 0o755)
	mustWrite(t, rc, "# user shell\n# >>> damnbot >>>\nexport PATH=\"$HOME/bin:$PATH\"\n# <<< damnbot <<<\n# user tail\n", 0o600)
	before := "# user shell\n# user tail\n"
	manifest := filepath.Join(data, ManifestName)
	m := &Manifest{
		Schema: 1, Version: "1.2.3", StateDir: data, InstalledAt: "2026-01-01T00:00:00Z",
		Files: []File{
			{Path: bin, Kind: "binary", SHA256After: hashOf(t, bin)},
			{Path: rc, Kind: "rc", SHA256After: hashOf(t, rc)},
		},
		Blocks: []Block{{Path: rc, Begin: BeginMarker, End: EndMarker}},
		Dirs:   []string{filepath.Dir(bin)},
	}
	if err := WriteManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %o, want 600", info.Mode().Perm())
	}

	var out bytes.Buffer
	rep, err := Uninstall(&Options{ManifestPath: manifest, DataDir: data, ConfigDir: cfg, HomeDir: home, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(bin); !os.IsNotExist(serr) {
		t.Fatalf("binary still present: %v", err)
	}
	got, err := os.ReadFile(rc) // #nosec G304 -- test temp rc path // #nosec G304 -- test temp rc path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != before {
		t.Fatalf("rc not byte-identical after uninstall:\n got %q\nwant %q", got, before)
	}
	if _, serr := os.Stat(manifest); !os.IsNotExist(serr) {
		t.Fatalf("manifest still present: %v", err)
	}
	if len(rep.Removed) == 0 || len(rep.Kept) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.Contains(out.String(), "data dir: "+data) {
		t.Fatalf("data dir not reported:\n%s", out.String())
	}
}

func TestUninstallKeepsUnlistedFiles(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	cfg := filepath.Join(home, "config")
	bin := filepath.Join(home, "bin", "damnbot")
	recorded := filepath.Join(home, "bin", "damnbot-completion")
	unlisted := filepath.Join(home, "bin", "user-tool")
	rcListed := filepath.Join(home, ".zshrc")
	rcUnlisted := filepath.Join(home, ".bashrc")
	for _, f := range []struct {
		path, body string
		mode       os.FileMode
	}{
		{bin, "binary", 0o755},
		{recorded, "completion", 0o644},
		{unlisted, "user", 0o755},
		{rcListed, "# zsh\n# >>> damnbot >>>\nblock\n# <<< damnbot <<<\n", 0o600},
		{rcUnlisted, "# bash user\n", 0o600},
	} {
		mustWrite(t, f.path, f.body, f.mode)
	}
	m := &Manifest{
		Schema: 1, Version: "1", StateDir: data, InstalledAt: "now",
		Files: []File{
			{Path: bin, Kind: "binary", SHA256After: hashOf(t, bin)},
			{Path: recorded, Kind: "completion", SHA256After: hashOf(t, recorded)},
			{Path: rcListed, Kind: "rc", SHA256After: hashOf(t, rcListed)},
		},
		Blocks: []Block{{Path: rcListed, Begin: BeginMarker, End: EndMarker}},
		Dirs:   []string{filepath.Dir(bin)},
	}
	manifest := filepath.Join(data, ManifestName)
	if err := WriteManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Uninstall(&Options{ManifestPath: manifest, DataDir: data, ConfigDir: cfg, HomeDir: home, Out: &out}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{unlisted, rcUnlisted} {
		if _, serr := os.Stat(path); serr != nil {
			t.Fatalf("unlisted file %s touched: %v", path, serr)
		}
	}
	got, err := os.ReadFile(rcListed) // #nosec G304 -- test temp rc path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# zsh\n" {
		t.Fatalf("listed rc = %q", got)
	}
	if _, serr := os.Stat(bin); !os.IsNotExist(serr) {
		t.Fatalf("recorded binary kept: %v", err)
	}
}

func TestUninstallKeepsChangedFile(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	bin := filepath.Join(home, "bin", "damnbot")
	mustWrite(t, bin, "user replaced", 0o755)
	m := &Manifest{
		Schema: 1, Version: "1", StateDir: data, InstalledAt: "now",
		Files: []File{{Path: bin, Kind: "binary", SHA256After: strings.Repeat("0", 64)}},
	}
	manifest := filepath.Join(data, ManifestName)
	if err := WriteManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rep, err := Uninstall(&Options{ManifestPath: manifest, DataDir: data, ConfigDir: data, HomeDir: home, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(bin); serr != nil {
		t.Fatalf("changed file was deleted: %v", serr)
	}
	if len(rep.Kept) != 1 || !strings.Contains(rep.Kept[0], "content changed") {
		t.Fatalf("kept = %v", rep.Kept)
	}
}

func TestUninstallDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	bin := filepath.Join(home, "bin", "damnbot")
	rc := filepath.Join(home, ".bashrc")
	mustWrite(t, bin, "binary", 0o755)
	block := "# >>> damnbot >>>\nexport PATH=\"$HOME/bin:$PATH\"\n# <<< damnbot <<<\n"
	mustWrite(t, rc, block, 0o600)
	m := &Manifest{
		Schema: 1, Version: "1", StateDir: data, InstalledAt: "now",
		Files:  []File{{Path: bin, Kind: "binary", SHA256After: hashOf(t, bin)}, {Path: rc, Kind: "rc", SHA256After: hashOf(t, rc)}},
		Blocks: []Block{{Path: rc, Begin: BeginMarker, End: EndMarker}},
	}
	manifest := filepath.Join(data, ManifestName)
	if err := WriteManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := Uninstall(&Options{ManifestPath: manifest, DryRun: true, DataDir: data, ConfigDir: data, HomeDir: home, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(bin); serr != nil {
		t.Fatalf("dry-run removed binary: %v", serr)
	}
	got, err := os.ReadFile(rc) // #nosec G304 -- test temp rc path // #nosec G304 -- test temp rc path
	if err != nil || string(got) != block {
		t.Fatalf("dry-run changed rc: %v %q", err, got)
	}
	if _, serr := os.Stat(manifest); serr != nil {
		t.Fatalf("dry-run removed manifest: %v", serr)
	}
	if !strings.Contains(out.String(), "--- "+rc) || !strings.Contains(out.String(), "-export PATH") {
		t.Fatalf("dry-run diff missing:\n%s", out.String())
	}
}

func TestManifestRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{"relative/path", "/tmp/../etc/passwd", "/"} {
		m := &Manifest{
			Schema: 1, StateDir: "/tmp", InstalledAt: "now",
			Files: []File{{Path: path, Kind: "binary", SHA256After: strings.Repeat("a", 64)}},
		}
		if err := WriteManifest(filepath.Join(t.TempDir(), ManifestName), m); err == nil {
			t.Fatalf("unsafe path %q accepted", path)
		}
	}
}

func TestRemoveBlockIdempotentAndPreservesBytes(t *testing.T) {
	b := Block{Begin: BeginMarker, End: EndMarker, AddedNewline: true}
	raw := []byte("user\n# >>> damnbot >>>\nbody\n# <<< damnbot <<<")
	next, found, err := removeBlock(raw, b)
	if err != nil || !found {
		t.Fatalf("removeBlock = %v %v", found, err)
	}
	if string(next) != "user" {
		t.Fatalf("removeBlock = %q", next)
	}
	again, found, err := removeBlock(next, b)
	if err != nil || found || string(again) != "user" {
		t.Fatalf("second removeBlock = %q %v %v", again, found, err)
	}
}

func mustWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func hashOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestInstallerRCMarkers(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	rc := filepath.Join(home, ".bashrc")
	block := BeginMarker + "\nexport PATH=\"$HOME/bin:$PATH\"\n" + EndMarker + "\n"
	mustWrite(t, rc, "# head\n"+block+"# tail\n", 0o600)
	m := &Manifest{
		Schema: 1, Version: "1", StateDir: data, InstalledAt: "now",
		Files:  []File{{Path: rc, Kind: "rc", SHA256After: hashOf(t, rc)}},
		Blocks: []Block{{Path: rc, Begin: BeginMarker, End: EndMarker}},
	}
	manifest := filepath.Join(data, ManifestName)
	if err := WriteManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(&Options{ManifestPath: manifest, DataDir: data, ConfigDir: data, HomeDir: home, Out: new(bytes.Buffer)}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(rc) // #nosec G304 -- test temp rc path // #nosec G304 -- test temp rc path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# head\n# tail\n" {
		t.Fatalf("markers not removed cleanly: %q", got)
	}
}

func TestUninstallRestoresRC(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	rc := filepath.Join(home, ".zshrc")
	before := "# one\n\n# two\n"
	mustWrite(t, rc, before+BeginMarker+"\nbody\n"+EndMarker+"\n", 0o600)
	m := &Manifest{
		Schema: 1, Version: "1", StateDir: data, InstalledAt: "now",
		Files:  []File{{Path: rc, Kind: "rc", SHA256After: hashOf(t, rc)}},
		Blocks: []Block{{Path: rc, Begin: BeginMarker, End: EndMarker}},
	}
	manifest := filepath.Join(data, ManifestName)
	if err := WriteManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(&Options{ManifestPath: manifest, DataDir: data, ConfigDir: data, HomeDir: home, Out: new(bytes.Buffer)}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(rc) // #nosec G304 -- test temp rc path // #nosec G304 -- test temp rc path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != before {
		t.Fatalf("rc not byte-identical: got %q want %q", got, before)
	}
}

func TestInstallManifest(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data")
	bin := filepath.Join(home, "bin", "damnbot")
	mustWrite(t, bin, "binary", 0o755)
	m := &Manifest{
		Schema: 1, Version: "1.2.3", StateDir: data, InstalledAt: "now",
		Files: []File{{Path: bin, Kind: "binary", SHA256After: hashOf(t, bin)}},
	}
	manifest := filepath.Join(data, ManifestName)
	if err := WriteManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %o", info.Mode().Perm())
	}
	got, err := ReadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.2.3" || len(got.Files) != 1 || got.Files[0].Path != bin {
		t.Fatalf("manifest round-trip = %+v", got)
	}
}

// TestUninstallPurgeData covers --purge-data end to end: both state dirs are
// removed after the recorded files, the manifest is gone, and the report says
// purged instead of kept.
func TestUninstallPurgeData(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, "data", "damnbot")
	cfg := filepath.Join(home, "config", "damnbot")
	bin := filepath.Join(home, "bin", "damnbot")
	mustWrite(t, filepath.Join(data, "damnbot.db"), "db", 0o600)
	mustWrite(t, filepath.Join(cfg, "config.toml"), "cfg", 0o600)
	mustWrite(t, bin, "binary", 0o755)
	manifest := filepath.Join(data, ManifestName)
	m := &Manifest{
		Schema: 1, Version: "1.2.3", StateDir: data, InstalledAt: "now",
		Files: []File{{Path: bin, Kind: "binary", SHA256After: hashOf(t, bin)}},
		Dirs:  []string{filepath.Dir(bin)},
	}
	if err := WriteManifest(manifest, m); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rep, err := Uninstall(&Options{ManifestPath: manifest, DataDir: data, ConfigDir: cfg, HomeDir: home, PurgeData: true, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Purged {
		t.Fatalf("report not purged: %+v", rep)
	}
	for _, dir := range []string{data, cfg} {
		if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
			t.Fatalf("%s still present after --purge-data: %v", dir, serr)
		}
	}
	if strings.Contains(out.String(), "kept; use --purge-data") {
		t.Fatalf("purge reported as kept:\n%s", out.String())
	}
}

// TestPurgeRefusesUnsafeDirs keeps --purge-data from ever removing HOME, an
// ancestor of HOME, a relative path, or a filesystem root.
func TestPurgeRefusesUnsafeDirs(t *testing.T) {
	home := t.TempDir()
	for name, dir := range map[string]string{
		"relative": "data",
		"root":     string(filepath.Separator),
		"home":     home,
		"ancestor": filepath.Dir(home),
	} {
		if err := purge(dir, home, io.Discard); err == nil {
			t.Errorf("purge(%s) accepted %q", name, dir)
		}
	}
	if err := purge("", home, io.Discard); err != nil {
		t.Fatalf("empty dir = %v, want a no-op", err)
	}
}
