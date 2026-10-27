package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// seedSkillRow writes one SKILL.md under the child's config dir and indexes it
// with a stored host requirement (skills.requires_host, ADR-208).
func seedSkillRow(t *testing.T, e *Env, name, requires string) {
	t.Helper()
	ctx := t.Context()
	dir := filepath.Join(e.ConfigDir(), "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + name + " skill\n---\nbody of " + name + "\n"
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(md))
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skillstore.Index(ctx, db, &skillstore.Row{
		Name: name, Description: name + " skill", Body: "body of " + name, Path: path,
		SHA256: hex.EncodeToString(sum[:]), Source: "user", RequiresHost: requires,
	}, 0); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSkillsListExcludesUnmetRequirements (GAP-TOOL-10): through the real
// binary, the skills_list catalog never offers a skill whose stored host
// requirement this host does not meet, while the negative control (requirement
// met, and no requirement at all) stays present.
func TestSkillsListExcludesUnmetRequirements(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "skills_list", `{}`),
		providertest.Text("done"),
	)
	seedSkillRow(t, e, "plan9only", `{"os":["plan9"]}`)
	seedSkillRow(t, e, "here", `{"os":["`+runtime.GOOS+`"]}`)
	seedSkillRow(t, e, "plain", "")
	seedSkillRow(t, e, "corrupt", `{"colour":true}`)
	e.Run("list the skills").RequireOK(t)
	req := e.RequestText(t, 1)
	for _, name := range []string{"- plan9only", "- corrupt"} {
		if strings.Contains(req, name) {
			t.Fatalf("skills_list offered %q (unmet or corrupt requirement):\n%s", name, req)
		}
	}
	for _, want := range []string{"- here: here skill", "- plain: plain skill"} {
		if !strings.Contains(req, want) {
			t.Errorf("skills_list missed %q (negative control):\n%s", want, req)
		}
	}
}
