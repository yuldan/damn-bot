package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func writeRuntimeHome(t *testing.T, e *Env) {
	t.Helper()
	root := filepath.Join(e.home, ".hermes")
	dir := filepath.Join(root, "skills", "external")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: external\ndescription: Needs Python\n---\nrun python3 -V\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestImportHermesRuntimeRequires(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeRuntimeHome(t, e)
	res := importExec(e, "import", "hermes")
	res.RequireOK(t)
	db := openImportDB(t, e)
	var req string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT requires_host FROM skills WHERE name = ?1`, []any{"external"}, &req)
	}); err != nil {
		t.Fatalf("read requires_host: %v", err)
	}
	if !strings.Contains(req, `"python3"`) {
		t.Fatalf("requires_host = %q", req)
	}
}

func TestImportHermesRuntimeWarningDryRun(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeRuntimeHome(t, e)
	emptyPath := t.TempDir()
	res := e.ExecEnv([]string{"PATH=" + emptyPath, "XDG_DATA_HOME=" + filepath.Join(e.home, "xdg-data")},
		"import", "hermes", "--dry-run")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, `warning: skill "external" requires runtime "python3" (not found)`) {
		t.Errorf("missing runtime warning:\n%s", res.Stdout)
	}
	requireNoDataDir(t, e)
}
