package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// writeHermesHome creates the minimal ~/.hermes fixture used by every import
// scenario: one skill, three memory entries and one unmapped config key.
func writeHermesHome(t *testing.T, e *Env) string {
	t.Helper()
	root := filepath.Join(e.home, ".hermes")
	files := map[string]string{
		"config.yaml":             "model:\n  default: openrouter/anthropic/claude-sonnet-4.5\n",
		"skills/deploy/SKILL.md":  "---\nname: deploy-helper\ndescription: Helps deploy the project.\n---\n# Deploy\nRun the deploy script.\n",
		"MEMORY.md":               "The build runs on linux.\n§\nKeep commits small.\n",
		"USER.md":                 "The user prefers table-driven tests.\n",
		"cron/jobs.json":          "[{\"id\":\"j1\",\"name\":\"nightly\",\"prompt\":\"summarize\",\"schedule\":\"0 3 * * *\"}]\n",
		"skills/ignored/.gitkeep": "",
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// importHermesDataDir mirrors the child's data dir with XDG_DATA_HOME pinned
// by importExec (Windows and macOS ignore XDG and use the harness home).
func importHermesDataDir(e *Env) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(e.home, "AppData", "Local", "damnbot")
	case "darwin":
		return filepath.Join(e.home, "Library", "Application Support", "damnbot")
	default:
		return filepath.Join(e.home, "xdg-data", "damnbot")
	}
}

// importExec runs `import` with a private XDG data dir so assertions can open
// the same database the child wrote.
func importExec(e *Env, args ...string) Result {
	e.t.Helper()
	return e.ExecEnv([]string{"XDG_DATA_HOME=" + filepath.Join(e.home, "xdg-data")}, args...)
}

// hashTree fingerprints every path, type, size and byte under root, so a dry
// run or a rejected source is proven not to have changed any state.
func hashTree(t *testing.T, root string) string {
	t.Helper()
	tree, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open %s: %v", root, err)
	}
	defer func() {
		if cerr := tree.Close(); cerr != nil {
			t.Errorf("close %s: %v", root, cerr)
		}
	}()
	h := sha256.New()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk: %w", walkErr)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return fmt.Errorf("relative path: %w", rerr)
		}
		info, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("stat: %w", ierr)
		}
		if _, werr := fmt.Fprintf(h, "%s|%v|%d|", filepath.ToSlash(rel), info.Mode().Type(), info.Size()); werr != nil {
			return fmt.Errorf("hash header: %w", werr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, rerr := tree.ReadFile(rel)
		if rerr != nil {
			return fmt.Errorf("read: %w", rerr)
		}
		if _, werr := h.Write(data); werr != nil {
			return fmt.Errorf("hash body: %w", werr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("hash %s: %v", root, err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func requireNoDataDir(t *testing.T, e *Env) {
	t.Helper()
	if _, err := os.Stat(importHermesDataDir(e)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("data dir exists after a write-free run: %v", err)
	}
}

func TestImportHermesWritesSkillsAndMemory(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeHermesHome(t, e)
	res := importExec(e, "import", "hermes")
	res.RequireOK(t)
	for _, want := range []string{
		"skills: 1 items, 1 imported, 1 changed, 0 skipped",
		"memory: 3 items, 3 imported, 3 changed, 0 skipped",
		"config: 1 items, 0 imported, 0 changed, 1 skipped",
		"skipped model.default: no config writer in F3 (SPEC §9.15)",
		"sessions: skipped: no sessions collector in F3 (SPEC §9.15)",
		"import: 6 items, 4 imported, 4 changed, 2 skipped, 100.0% accounted",
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("import output missing %q:\n%s", want, res.Stdout)
		}
	}
	// The quarantined skill is visible in the store as an import row linked
	// to a review memory id, never as an approved skill.
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: importHermesDataDir(e), Kind: "cli"})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	}()
	skills, err := db.Count(t.Context(),
		`SELECT count(*) FROM skills WHERE name = 'deploy-helper' AND source = 'import' AND memory_id IS NOT NULL`)
	if err != nil {
		t.Fatalf("count skills: %v", err)
	}
	if skills != 1 {
		t.Errorf("quarantined skill rows = %d, want 1", skills)
	}
	scanned, err := db.Count(t.Context(),
		`SELECT count(*) FROM v_memory_status WHERE kind = 'memory' AND origin = 'import' AND scope = 'user' AND status = 'scanned'`)
	if err != nil {
		t.Fatalf("count memory: %v", err)
	}
	if scanned != 3 {
		t.Errorf("scanned memory rows = %d, want 3", scanned)
	}
	// The user-facing queue shows the imported entries.
	review := e.ExecEnv([]string{"XDG_DATA_HOME=" + filepath.Join(e.home, "xdg-data")}, "review")
	review.RequireOK(t)
	for _, want := range []string{"The build runs on linux.", "Keep commits small.", "table-driven tests"} {
		if !strings.Contains(review.Stdout, want) {
			t.Errorf("review missing %q:\n%s", want, review.Stdout)
		}
	}
}

func TestImportHermesIdempotent(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeHermesHome(t, e)
	first := importExec(e, "import", "hermes")
	first.RequireOK(t)
	second := importExec(e, "import", "hermes")
	second.RequireOK(t)
	if !strings.Contains(second.Stdout, "skills: 1 items, 1 imported, 0 changed, 0 skipped") ||
		!strings.Contains(second.Stdout, "memory: 3 items, 3 imported, 0 changed, 0 skipped") {
		t.Errorf("second run is not idempotent:\n%s", second.Stdout)
	}
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: importHermesDataDir(e), Kind: "cli"})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close db: %v", cerr)
		}
	}()
	skills, err := db.Count(t.Context(), `SELECT count(*) FROM skills WHERE name = 'deploy-helper'`)
	if err != nil {
		t.Fatalf("count skills: %v", err)
	}
	memories, err := db.Count(t.Context(),
		`SELECT count(*) FROM v_memory_status WHERE kind = 'memory' AND origin = 'import'`)
	if err != nil {
		t.Fatalf("count memory: %v", err)
	}
	if skills != 1 || memories != 3 {
		t.Errorf("after two runs: skills = %d, memories = %d; want 1 and 3", skills, memories)
	}
}

func TestImportHermesDryRunWritesNothing(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeHermesHome(t, e)
	before := hashTree(t, e.home)
	res := importExec(e, "import", "hermes", "--dry-run")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "dry run: nothing was written") {
		t.Errorf("dry run output:\n%s", res.Stdout)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("dry run changed state: %s -> %s", before, after)
	}
	requireNoDataDir(t, e)
}

func TestImportHermesSymlinkEscapeRejected(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := writeHermesHome(t, e)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"),
		[]byte("---\nname: escaped\ndescription: escaped\n---\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A SKILL.md that is a link out of the tree must be rejected before it is
	// read, not followed.
	evil := filepath.Join(root, "skills", "escaped")
	if err := os.MkdirAll(evil, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(evil, "SKILL.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	before := hashTree(t, e.home)
	res := importExec(e, "import", "hermes")
	if res.Code == 0 {
		t.Fatalf("escaping symlink imported, exit 0:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "import hermes:") {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("rejected import changed state: %s -> %s", before, after)
	}
	requireNoDataDir(t, e)
}

func TestImportHermesOnlyUnsupportedExitsTwo(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeHermesHome(t, e)
	before := hashTree(t, e.home)
	res := importExec(e, "import", "hermes", "--only", "config")
	if res.Code != 2 {
		t.Fatalf("exit = %d, want 2:\n%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "SPEC §9.15") {
		t.Errorf("stderr does not name the SPEC section: %q", res.Stderr)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("--only config changed state: %s -> %s", before, after)
	}
	requireNoDataDir(t, e)
}
