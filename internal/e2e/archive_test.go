package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestArchivePortableBinary drives the whole GAP-OPS-05 flow through the real
// binary: `sessions backup create --portable` writes a DMBK1 archive and
// `import archive` merges it into a second home, where skills and memory land
// in quarantine after the threat scan.
func TestArchivePortableBinary(t *testing.T) { //nolint:gocyclo // one linear end-to-end scenario
	t.Parallel()
	src := New(t)
	dataHome := filepath.Join(src.home, "xdg-data")
	dataEnv := []string{"XDG_DATA_HOME=" + dataHome}
	srcPaths := resolvedPaths(t, src, dataEnv)
	cfgDir := srcPaths.ConfigDir
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("[ui]\nreply_language = \"auto\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "skills", "deploy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "skills", "deploy", "SKILL.md"),
		[]byte("---\nname: deploy\ndescription: Deploy the project.\n---\nRun the deploy.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hermes := filepath.Join(src.home, ".hermes")
	if err := os.MkdirAll(hermes, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hermes, "MEMORY.md"), []byte("the project uses tabs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src.ExecEnv(dataEnv, "import", "hermes", "--from", hermes).RequireOK(t)
	src.ExecEnv(dataEnv, "review", "approve", "--bulk", "--data-dir", srcPaths.DataDir).RequireOK(t)

	archive := src.Path("portable.dmbk")
	created := src.Exec("sessions", "backup", "create", "--portable", "--data-dir", srcPaths.DataDir, "--out", archive)
	created.RequireOK(t)
	if !strings.Contains(created.Stdout, "archive:") || !strings.Contains(created.Stdout, "skills=1") {
		t.Fatalf("export output:\n%s", created.Stdout)
	}
	assertNoCanary(t, archive, "the project uses tabs")

	dst := New(t)
	dstPaths := resolvedPaths(t, dst, nil)
	copyBackupKey(t, srcPaths.ConfigDir, dstPaths.ConfigDir)
	imported := dst.Exec("import", "archive", archive, "--data-dir", dst.DataDir)
	imported.RequireOK(t)
	if !strings.Contains(imported.Stdout, "skills=1") || !strings.Contains(imported.Stdout, "memory=1") {
		t.Fatalf("import output:\n%s", imported.Stdout)
	}
	cfg, err := os.ReadFile(filepath.Join(dstPaths.ConfigDir, "config.toml"))
	if err != nil || string(cfg) != "[ui]\nreply_language = \"auto\"\n" {
		t.Fatalf("imported config = %q, %v", cfg, err)
	}
	db := openDataDirDB(t, dst.DataDir)
	if n := countRows(t, db, `SELECT count(*) FROM skills WHERE name = 'deploy' AND memory_id IS NOT NULL`); n != 1 {
		t.Fatalf("quarantined skills = %d, want 1", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM v_memory_status WHERE kind = 'memory' AND origin = 'import' AND status = 'scanned'`); n != 1 {
		t.Fatalf("quarantined memory = %d, want 1", n)
	}
	again := dst.Exec("import", "archive", archive, "--data-dir", dst.DataDir)
	again.RequireOK(t)
	if !strings.Contains(again.Stdout, "changed=0") {
		t.Fatalf("second import is not idempotent:\n%s", again.Stdout)
	}
	// Keep-local leaves a conflicting local config untouched.
	if werr := os.WriteFile(filepath.Join(dstPaths.ConfigDir, "config.toml"), []byte("[ui]\nreply_language = \"en\"\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	keep := dst.Exec("import", "archive", archive, "--data-dir", dst.DataDir, "--conflict", "keep-local")
	keep.RequireOK(t)
	cfg, err = os.ReadFile(filepath.Join(dstPaths.ConfigDir, "config.toml"))
	if err != nil || string(cfg) != "[ui]\nreply_language = \"en\"\n" {
		t.Fatalf("keep-local overwrote config: %q", cfg)
	}
	take := dst.Exec("import", "archive", archive, "--data-dir", dst.DataDir, "--conflict", "take-remote")
	take.RequireOK(t)
	cfg, err = os.ReadFile(filepath.Join(dstPaths.ConfigDir, "config.toml"))
	if err != nil || string(cfg) != "[ui]\nreply_language = \"auto\"\n" {
		t.Fatalf("take-remote kept local config: %q", cfg)
	}
}

// TestArchivePortableBinaryRefusals checks the negative paths through the
// binary: a config secret and a non-DMBK1 file are both refused.
func TestArchivePortableBinaryRefusals(t *testing.T) {
	t.Parallel()
	e := New(t)
	cfgDir := resolvedPaths(t, e, nil).ConfigDir
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("api_key = \"ghp_"+strings.Repeat("A", 36)+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := e.Exec("sessions", "backup", "create", "--portable", "--out", e.Path("bad.dmbk"))
	if bad.Code == 0 || !strings.Contains(bad.Stderr, "plaintext secrets") {
		t.Fatalf("secret config export = %d\n%s", bad.Code, bad.Stderr)
	}
	garbage := e.Path("garbage.dmbk")
	if err := os.WriteFile(garbage, []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := e.Exec("import", "archive", garbage, "--data-dir", e.DataDir); res.Code == 0 {
		t.Fatalf("garbage archive accepted:\n%s", res.Stdout)
	}
}

// dumpPaths is the path half of `damnbot dump --format json` (CLI-036).
type dumpPaths struct {
	ConfigDir string `json:"config_dir"`
	DataDir   string `json:"data_dir"`
}

// resolvedPaths asks the built binary where it resolves its config and data
// directories under env. Hard-coding the fixture path is how the Windows run
// broke: XDG_DATA_HOME is a Linux convention, so `import hermes` wrote to
// LOCALAPPDATA while --data-dir pointed at <home>/xdg-data and the approved
// memory never reached the exported database (first import reported memory=0).
func resolvedPaths(t *testing.T, e *Env, env []string) dumpPaths {
	t.Helper()
	res := e.ExecEnv(env, "dump", "--format", "json")
	res.RequireOK(t)
	var p dumpPaths
	if err := json.Unmarshal([]byte(res.Stdout), &p); err != nil {
		t.Fatalf("dump paths: %v\n%s", err, res.Stdout)
	}
	p.ConfigDir = e.expandDumpHome(p.ConfigDir)
	p.DataDir = e.expandDumpHome(p.DataDir)
	if p.ConfigDir == "" || p.DataDir == "" {
		t.Fatalf("dump returned no paths: %+v", p)
	}
	if got, want := filepath.Clean(p.ConfigDir), filepath.Clean(spillConfigDir(e.home)); got != want {
		t.Fatalf("dump config dir = %s, harness home fixture = %s", got, want)
	}
	return p
}

// expandDumpHome undoes the support summary's home redaction (`dump` replaces
// os.UserHomeDir with "~"); the fixture home is what the child was given.
func (e *Env) expandDumpHome(p string) string {
	if p == "~" {
		return e.home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(e.home, p[2:])
	}
	return p
}

func copyBackupKey(t *testing.T, from, to string) {
	t.Helper()
	src := filepath.Join(from, "backup.keys")
	data, err := os.ReadFile(src) //nolint:gosec // test fixture path
	if err != nil {
		if os.IsNotExist(err) {
			return // keyring backend: both children share the OS keychain
		}
		t.Fatal(err)
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "backup.keys"), data, 0o600); err != nil { //nolint:gosec // test-owned home
		t.Fatal(err)
	}
}

func openDataDirDB(t *testing.T, dir string) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	})
	return db
}
