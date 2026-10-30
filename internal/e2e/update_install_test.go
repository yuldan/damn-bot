package e2e

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func updateInstallEnv(t *testing.T) *Env {
	t.Helper()
	e := newEnv(t)
	dir := e.ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := []byte("[provider]\nmodel = \"openai/gpt-4o\"\n")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(t.TempDir(), filepath.Base(binPaths.main))
	if err := os.Link(binPaths.main, app); err != nil {
		t.Fatalf("link old binary: %v", err)
	}
	e.bin = app
	return e
}

func readTestBytes(t *testing.T, path string) []byte {
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
	return data
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func buildAssetBinary(t *testing.T) []byte {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), filepath.Base(binPaths.main))
	if out, berr := buildDamnbot(root, path, "-X main.version=e2e-2.0.0"); berr != nil {
		t.Fatalf("build asset v2: %v\n%s", berr, out)
	}
	return readTestBytes(t, path)
}

func assetNameForTest(version string) string {
	name := "damnbot_" + version + "_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func updateFeed(t *testing.T, asset []byte, corrupt bool) (string, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	name := assetNameForTest("2.0.0")
	man := map[string]any{
		"schema": 1, "channel": "stable", "version": "2.0.0",
		"commit": strings.Repeat("1", 40), "published_at": "2026-10-27T00:00:00Z",
		"changelog": "- e2e install",
		"assets": []map[string]string{{
			"os": runtime.GOOS, "arch": runtime.GOARCH, "name": name, "sha256": hashBytes(asset),
		}},
	}
	body, err := json.Marshal(man)
	if err != nil {
		t.Fatal(err)
	}
	sig := "damnbot-signature/v1 ed25519\n" + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)) + "\n"
	served := asset
	if corrupt {
		served = append([]byte(nil), asset...)
		served[0] ^= 0xff
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "v2.0.0",
			"assets": []map[string]string{
				{"name": "release.json", "browser_download_url": srv.URL + "/release.json"},
				{"name": "release.json.sig", "browser_download_url": srv.URL + "/release.json.sig"},
				{"name": name, "browser_download_url": srv.URL + "/asset"},
			},
		}}); err != nil {
			t.Errorf("encode releases: %v", err)
		}
	})
	mux.HandleFunc("/release.json", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(body); err != nil {
			t.Errorf("write manifest: %v", err)
		}
	})
	mux.HandleFunc("/release.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(sig)); err != nil {
			t.Errorf("write signature: %v", err)
		}
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(served); err != nil {
			t.Errorf("write asset: %v", err)
		}
	})
	return srv.URL + "/releases", pub
}

func updateEnvForFeed(pub ed25519.PublicKey) []string {
	return []string{"DAMNBOT_RELEASE_PUBKEY=" + base64.StdEncoding.EncodeToString(pub)}
}

func writeUpdateFeedConfig(t *testing.T, e *Env, feed string) {
	t.Helper()
	cfg := []byte("[provider]\nmodel = \"openai/gpt-4o\"\n\n[update]\nfeed_url = \"" + feed + "\"\n")
	if err := os.WriteFile(filepath.Join(e.ConfigDir(), "config.toml"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
}

func findPending(t *testing.T, home string) string {
	t.Helper()
	found := ""
	if err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "pending.json" {
			found = path
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

func versionAt(t *testing.T, e *Env, env []string, path string) string {
	t.Helper()
	oldBin := e.bin
	e.bin = path
	defer func() { e.bin = oldBin }()
	res := e.ExecEnv(env, "--version")
	t.Logf("--version %s: exit=%d stdout=%q stderr=%q", path, res.Code, strings.TrimSpace(res.Stdout), strings.TrimSpace(res.Stderr))
	if res.Code != 0 {
		t.Fatalf("%s --version exit %d: %s", path, res.Code, res.Stderr)
	}
	return res.Stdout
}

func TestE2EUpdateInstallAndRollback(t *testing.T) {
	e := updateInstallEnv(t)
	old := readTestBytes(t, e.bin)
	asset := buildAssetBinary(t)
	if hashBytes(asset) == hashBytes(old) {
		t.Fatal("asset binary is identical to old; test needs a different build")
	}
	feed, pub := updateFeed(t, asset, false)
	writeUpdateFeedConfig(t, e, feed)
	env := updateEnvForFeed(pub)
	if res := e.ExecEnv(env, "update"); res.Code != 0 {
		t.Fatalf("install exit %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	assertInstalled(t, e, env, old, asset)
	if res := e.ExecEnv(env, "update", "--rollback"); res.Code != 0 {
		t.Fatalf("rollback exit %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	assertRolledBack(t, e, env, old)
}

func assertInstalled(t *testing.T, e *Env, env []string, old, asset []byte) {
	t.Helper()
	activeHash := hashBytes(readTestBytes(t, e.bin))
	prevHash := hashBytes(readTestBytes(t, e.bin+".prev"))
	t.Logf("install: old=%s asset=%s active=%s prev=%s", hashBytes(old), hashBytes(asset), activeHash, prevHash)
	if activeHash != hashBytes(asset) {
		t.Fatalf("active hash = %s, want asset %s", activeHash, hashBytes(asset))
	}
	if prevHash != hashBytes(old) {
		t.Fatalf("prev hash = %s, want old %s", prevHash, hashBytes(old))
	}
	pending := findPending(t, e.home)
	if pending == "" {
		t.Fatal("pending.json not written")
	}
	if body := string(readTestBytes(t, pending)); !strings.Contains(body, `"target_version":"2.0.0"`) ||
		!strings.Contains(body, `"prev_version":"dev"`) || !strings.Contains(body, `"premigrate_db"`) {
		t.Fatalf("pending = %s", body)
	}
	if out := versionAt(t, e, env, e.bin); !strings.Contains(out, "e2e-2.0.0") {
		t.Fatalf("installed version = %q, want e2e-2.0.0", out)
	}
	prevPath := e.bin + ".prev"
	if runtime.GOOS == "windows" {
		prevPath += ".exe" // CreateProcess needs the executable extension
		if err := os.Link(e.bin+".prev", prevPath); err != nil {
			t.Fatalf("link prev for execution: %v", err)
		}
	}
	if out := versionAt(t, e, env, prevPath); !strings.Contains(out, "damnbot dev") {
		t.Fatalf("prev version = %q, want dev", out)
	}
}

func assertRolledBack(t *testing.T, e *Env, env []string, old []byte) {
	t.Helper()
	restoredHash := hashBytes(readTestBytes(t, e.bin))
	t.Logf("rollback: restored=%s old=%s", restoredHash, hashBytes(old))
	if restoredHash != hashBytes(old) {
		t.Fatalf("rolled-back hash = %s, want old %s", restoredHash, hashBytes(old))
	}
	if _, err := os.Stat(e.bin + ".prev"); !os.IsNotExist(err) {
		t.Fatalf("prev still exists after rollback: %v", err)
	}
	if pending := findPending(t, e.home); pending != "" {
		t.Fatalf("pending still exists after rollback: %s", pending)
	}
	if out := versionAt(t, e, env, e.bin); !strings.Contains(out, "damnbot dev") {
		t.Fatalf("rolled-back version = %q, want dev", out)
	}
}

func TestE2EUpdateInstallRejectsCorruptAsset(t *testing.T) {
	e := updateInstallEnv(t)
	old := readTestBytes(t, e.bin)
	asset := buildAssetBinary(t)
	feed, pub := updateFeed(t, asset, true)
	writeUpdateFeedConfig(t, e, feed)
	res := e.ExecEnv(updateEnvForFeed(pub), "update")
	if res.Code == 0 {
		t.Fatalf("corrupt asset installed\nstdout: %s", res.Stdout)
	}
	activeHash := hashBytes(readTestBytes(t, e.bin))
	t.Logf("corrupt asset: old=%s active=%s", hashBytes(old), activeHash)
	if activeHash != hashBytes(old) {
		t.Fatalf("active binary changed: %s != %s", activeHash, hashBytes(old))
	}
	if _, err := os.Stat(e.bin + ".prev"); !os.IsNotExist(err) {
		t.Fatal("prev written on corrupt asset")
	}
	if pending := findPending(t, e.home); pending != "" {
		t.Fatalf("pending written on corrupt asset: %s", pending)
	}
}

// TestE2EUpdateRepairRollsBackCorruptInstall: an interrupted install whose
// active binary no longer matches the signed manifest is repaired by restoring
// .prev; the data directory is untouched.
func TestE2EUpdateRepairRollsBackCorruptInstall(t *testing.T) {
	e := updateInstallEnv(t)
	old := readTestBytes(t, e.bin)
	asset := buildAssetBinary(t)
	feed, pub := updateFeed(t, asset, false)
	writeUpdateFeedConfig(t, e, feed)
	env := updateEnvForFeed(pub)
	if res := e.ExecEnv(env, "update"); res.Code != 0 {
		t.Fatalf("install exit %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	pending := findPending(t, e.home)
	if pending == "" {
		t.Fatal("pending.json not written")
	}
	dbPath, staged := corruptInstalledBinary(t, e, pending, old)
	res := e.ExecEnv(env, "update", "--repair")
	if res.Code != 0 || !strings.Contains(res.Stdout, "rolled back") {
		t.Fatalf("repair exit %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	assertRepaired(t, e, env, old, staged, dbPath)
}

func corruptInstalledBinary(t *testing.T, e *Env, pending string, old []byte) (dbPath, staged string) {
	t.Helper()
	dataDir := filepath.Dir(filepath.Dir(pending))
	dbPath = filepath.Join(dataDir, "damnbot.db")
	if err := os.WriteFile(dbPath, []byte("db-must-survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The active binary is back at the old version and a corrupt staged asset
	// remains: exactly the state repair must recover without touching the DB.
	f, err := os.OpenFile(e.bin, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(old); err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod(0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	staged = filepath.Join(filepath.Dir(e.bin), "damnbot.new"+filepath.Ext(e.bin))
	if err := os.WriteFile(staged, []byte("corrupt-asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dbPath, staged
}

func assertRepaired(t *testing.T, e *Env, env []string, old []byte, staged, dbPath string) {
	t.Helper()
	if got := hashBytes(readTestBytes(t, e.bin)); got != hashBytes(old) {
		t.Fatalf("repaired active = %s, want old %s", got, hashBytes(old))
	}
	if findPending(t, e.home) != "" {
		t.Fatal("pending.json survived repair")
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged corrupt asset survived repair: %v", err)
	}
	if got := string(readTestBytes(t, dbPath)); got != "db-must-survive" {
		t.Fatalf("repair changed the database: %q", got)
	}
	if out := versionAt(t, e, env, e.bin); !strings.Contains(out, "damnbot dev") {
		t.Fatalf("repaired binary version = %q, want dev", out)
	}
}
