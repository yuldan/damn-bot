package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/netx"
)

type installFixture struct {
	dir       string
	dataDir   string
	binary    string
	newBytes  []byte
	feedURL   string
	pub       ed25519.PublicKey
	version   string
	srv       *httptest.Server
	body      []byte
	sig       []byte
	truncated bool
}

func newInstallFixture(t *testing.T, version string) *installFixture {
	t.Helper()
	priv, pub := testKey(t)
	dir := t.TempDir()
	f := &installFixture{
		dir: dir, dataDir: filepath.Join(dir, "data"),
		binary: filepath.Join(dir, "app", "damnbot"), newBytes: []byte("new-binary-content"), pub: pub, version: version,
	}
	if runtime.GOOS == "windows" {
		f.binary += ".exe"
	}
	for _, d := range []string{filepath.Dir(f.binary), f.dataDir, filepath.Join(f.dataDir, "backups")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.binary, []byte("old-binary-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dataDir, "backups", "premigrate-1-2.db"), []byte("old-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	man, binaryName := installManifest(t, version, f.newBytes)
	f.body, f.sig = signed(t, priv, &man)
	f.srv = installServer(t, f, binaryName)
	f.feedURL = f.srv.URL + "/releases"
	return f
}

func installManifest(t *testing.T, version string, binary []byte) (manifest, string) {
	t.Helper()
	man := testManifest(version, channelStable)
	sum := sha256.Sum256(binary)
	binaryName := assetName(version, runtime.GOOS, mustArch(t))
	for i := range man.Assets {
		if man.Assets[i].OS == runtime.GOOS && man.Assets[i].Arch == mustArch(t) {
			man.Assets[i].SHA256 = hex.EncodeToString(sum[:])
			man.Assets[i].Name = binaryName
		}
	}
	return man, binaryName
}

func installServer(t *testing.T, f *installFixture, binaryName string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode([]ghRelease{{TagName: "v" + f.version, Assets: []ghAsset{
			{Name: manifestAsset, BrowserDownloadURL: srv.URL + "/release.json"},
			{Name: manifestSigAsset, BrowserDownloadURL: srv.URL + "/release.json.sig"},
			{Name: binaryName, BrowserDownloadURL: srv.URL + "/binary"},
		}}}); err != nil {
			t.Errorf("encode releases: %v", err)
		}
	})
	mux.HandleFunc("/release.json", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(f.body); err != nil {
			t.Errorf("write manifest: %v", err)
		}
	})
	mux.HandleFunc("/release.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(f.sig); err != nil {
			t.Errorf("write signature: %v", err)
		}
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, _ *http.Request) {
		if f.truncated {
			w.Header().Set("Content-Length", "1000")
			if _, err := w.Write([]byte("half")); err != nil {
				t.Errorf("write half: %v", err)
			}
			return
		}
		if _, err := w.Write(f.newBytes); err != nil {
			t.Errorf("write binary: %v", err)
		}
	})
	return srv
}

func mustArch(t *testing.T) string {
	t.Helper()
	a, err := nativeArch()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *installFixture) options(current string, health func(string) error) *InstallOptions {
	return &InstallOptions{
		Client:     netx.New(netx.Config{}).Client("update", netx.Background),
		FeedURL:    f.feedURL,
		Channel:    channelStable,
		Current:    current,
		PublicKey:  f.pub,
		DataDir:    f.dataDir,
		BinaryPath: f.binary,
		HealthCheck: func(_ context.Context, path string) error {
			if health == nil {
				return nil
			}
			return health(path)
		},
	}
}

func (f *installFixture) read(t *testing.T, path string) string {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close root: %v", cerr)
		}
	}()
	data, err := root.ReadFile(filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInstallSuccess(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	target, err := Install(t.Context(), f.options("1.0.0", nil))
	if err != nil || target != "2.0.0" {
		t.Fatalf("Install = %q, %v", target, err)
	}
	if got := f.read(t, f.binary); got != string(f.newBytes) {
		t.Fatalf("active binary = %q", got)
	}
	if got := f.read(t, f.binary+".prev"); got != "old-binary-content" {
		t.Fatalf("prev binary = %q", got)
	}
	p, err := readPending(f.dataDir)
	if err != nil || p.TargetVersion != "2.0.0" || p.PrevVersion != "1.0.0" || p.PremigrateDB == "" {
		t.Fatalf("pending = %+v, %v", p, err)
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	old := f.read(t, f.binary)
	// Re-sign a manifest whose asset hash is wrong; the binary is untouched.
	priv, _ := testKey(t)
	_ = priv
	// Replace the served binary with different bytes after the manifest was signed.
	if err := os.WriteFile(filepath.Join(f.dir, "other"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fixture handler serves f.newBytes; change it to different bytes.
	f.newBytes = []byte("tampered-binary")
	_, err := Install(t.Context(), f.options("1.0.0", nil))
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("checksum mismatch: %v", err)
	}
	if f.read(t, f.binary) != old {
		t.Fatal("old binary changed on checksum failure")
	}
	if _, err := os.Stat(f.binary + ".prev"); !os.IsNotExist(err) {
		t.Fatal("prev written on checksum failure")
	}
	if _, err := readPending(f.dataDir); !errors.Is(err, ErrNoPending) {
		t.Fatal("pending written on checksum failure")
	}
}

func TestInstallRejectsBadSignature(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	old := f.read(t, f.binary)
	otherPriv, _ := testKey(t)
	_, otherSig := signed(t, otherPriv, &manifest{Schema: 1, Channel: channelStable, Version: "9.9.9", Changelog: "x", Assets: testAssets("9.9.9")})
	f.sig = otherSig
	_, err := Install(t.Context(), f.options("1.0.0", nil))
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("bad signature: %v", err)
	}
	if f.read(t, f.binary) != old {
		t.Fatal("old binary changed on signature failure")
	}
}

func TestInstallRejectsNotNewer(t *testing.T) {
	f := newInstallFixture(t, "1.0.0")
	if _, err := Install(t.Context(), f.options("1.0.0", nil)); !errors.Is(err, errNotNewer) {
		t.Fatalf("equal version = %v, want errNotNewer", err)
	}
	if _, err := Install(t.Context(), f.options("2.0.0", nil)); !errors.Is(err, errNotNewer) {
		t.Fatalf("older target = %v, want errNotNewer", err)
	}
}

func TestInstallInterruptedDownload(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	old := f.read(t, f.binary)
	f.truncated = true
	if _, err := Install(t.Context(), f.options("1.0.0", nil)); err == nil {
		t.Fatal("interrupted download succeeded")
	}
	if f.read(t, f.binary) != old {
		t.Fatal("old binary changed on interrupted download")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.binary), "damnbot.new")); !os.IsNotExist(err) {
		t.Fatal("new binary left after interrupted download")
	}
	if _, err := readPending(f.dataDir); !errors.Is(err, ErrNoPending) {
		t.Fatal("pending left after interrupted download")
	}
}

func TestRollbackRestoresBinaryAndDB(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	if _, err := Install(t.Context(), f.options("1.0.0", nil)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dataDir, "damnbot.db"), []byte("new-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(f.dataDir, true); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := f.read(t, f.binary); got != "old-binary-content" {
		t.Fatalf("restored binary = %q", got)
	}
	if got := f.read(t, filepath.Join(f.dataDir, "damnbot.db")); got != "old-db" {
		t.Fatalf("restored DB = %q", got)
	}
	if _, err := readPending(f.dataDir); !errors.Is(err, ErrNoPending) {
		t.Fatal("pending not removed after rollback")
	}
}

func TestRollbackNeedsPair(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	if err := Rollback(f.dataDir, false); !errors.Is(err, ErrNoPending) {
		t.Fatalf("missing pending = %v", err)
	}
	if _, err := Install(t.Context(), f.options("1.0.0", nil)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.dataDir, "backups", "premigrate-1-2.db")); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(f.dataDir, true); !errors.Is(err, ErrNoPending) {
		t.Fatalf("missing DB pair = %v", err)
	}
}

func TestInstallSerializesConcurrentUpdates(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var healthErr error
	health := func(string) error {
		once.Do(func() { close(started) })
		<-release
		return healthErr
	}
	done := make(chan error, 1)
	go func() {
		_, err := Install(t.Context(), f.options("1.0.0", health))
		done <- err
	}()
	<-started
	if _, err := Install(t.Context(), f.options("1.0.0", nil)); !errors.Is(err, ErrLocked) {
		t.Fatalf("second updater = %v, want ErrLocked", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first updater: %v", err)
	}
}

// TestRepairRollsBackCorruptInstall pins OPS-028: a pending record whose
// installed binary no longer matches the signed manifest restores .prev and
// leaves the data directory alone.
func TestRepairRollsBackCorruptInstall(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	opts := f.options("1.0.0", nil)
	if _, err := Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dataDir, "damnbot.db"), []byte("db-intact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.binary, []byte("corrupt-asset"), 0o755); err != nil {
		t.Fatal(err)
	}
	status, err := Repair(t.Context(), opts)
	if err != nil || !strings.Contains(status, "rolled back") {
		t.Fatalf("Repair = %q, %v", status, err)
	}
	if got := f.read(t, f.binary); got != "old-binary-content" {
		t.Fatalf("repaired binary = %q", got)
	}
	if got := f.read(t, filepath.Join(f.dataDir, "damnbot.db")); got != "db-intact" {
		t.Fatalf("repair changed the database: %q", got)
	}
	if _, err := readPending(f.dataDir); !errors.Is(err, ErrNoPending) {
		t.Fatal("pending survived repair")
	}
}

// TestRepairRejectsTamperedPending rolls back when the recorded target no
// longer matches the signed manifest, instead of trusting the corrupt target.
func TestRepairRejectsTamperedPending(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	opts := f.options("1.0.0", nil)
	if _, err := Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	p, err := readPending(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	p.TargetSHA256 = strings.Repeat("f", 64)
	if err := writePending(f.dataDir, &p); err != nil {
		t.Fatal(err)
	}
	status, err := Repair(t.Context(), opts)
	if err != nil || !strings.Contains(status, "rolled back") {
		t.Fatalf("Repair tampered = %q, %v", status, err)
	}
	if got := f.read(t, f.binary); got != "old-binary-content" {
		t.Fatalf("repaired binary = %q", got)
	}
}

// TestRepairWithoutPendingFailsClosed: nothing to repair is ErrNoPending.
func TestRepairWithoutPendingFailsClosed(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	if _, err := Repair(t.Context(), f.options("1.0.0", nil)); !errors.Is(err, ErrNoPending) {
		t.Fatalf("Repair without pending = %v, want ErrNoPending", err)
	}
}

// TestRepairFinalizesHealthy: a completed swap whose active binary matches the
// signed manifest only clears pending.
func TestRepairFinalizesHealthy(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	opts := f.options("1.0.0", nil)
	if _, err := Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	status, err := Repair(t.Context(), opts)
	if err != nil || !strings.Contains(status, "verified 2.0.0") {
		t.Fatalf("Repair healthy = %q, %v", status, err)
	}
	if got := f.read(t, f.binary); got != string(f.newBytes) {
		t.Fatalf("healthy repair changed the binary: %q", got)
	}
}

// TestRepairRestoresMissingActive covers a crash between the two swap renames.
func TestRepairRestoresMissingActive(t *testing.T) {
	f := newInstallFixture(t, "2.0.0")
	opts := f.options("1.0.0", nil)
	if _, err := Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.binary); err != nil {
		t.Fatal(err)
	}
	status, err := Repair(t.Context(), opts)
	if err != nil || !strings.Contains(status, "rolled back") {
		t.Fatalf("Repair missing active = %q, %v", status, err)
	}
	if got := f.read(t, f.binary); got != "old-binary-content" {
		t.Fatalf("restored binary = %q", got)
	}
}
