package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestImportHermesPreservesSkillRequires(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := writeHermesHome(t, e)
	dir := filepath.Join(root, "skills", "external-doc")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: external-doc\ndescription: Needs Python\nrequires: {bins: [python3@3.11]}\n---\nrun the script\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	first := importExec(e, "import", "hermes")
	first.RequireOK(t)
	db := openImportDB(t, e)
	var req string
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT requires_host FROM skills WHERE name = ?1`, []any{"external-doc"}, &req)
	}); err != nil {
		t.Fatalf("read requires_host: %v", err)
	}
	if !strings.Contains(req, `"python3"`) {
		t.Fatalf("requires_host = %q", req)
	}
	again := importExec(e, "import", "hermes")
	again.RequireOK(t)
}
