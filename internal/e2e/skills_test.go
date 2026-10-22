package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestSkillsTools (TOOL-074/075, SKILL-002): the real binary calls
// skills_list and skill_view for an approved skill. Approved text reaches the
// model only wrapped as <untrusted> data.
func TestSkillsTools(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "skills_list", `{}`),
		ToolCallReply("c2", "skill_view", `{"name":"reviewed"}`),
		providertest.Text("done"),
	)
	body := "APPROVED-SKILL-BODY"
	seedApproved(t, e, "reviewed", "approved skill", body)
	res := e.Run("list and view skills")
	res.RequireOK(t)
	if got := len(e.Requests()); got != 3 {
		t.Fatalf("provider calls = %d, want 3", got)
	}
	if req := e.RequestText(t, 1); !strings.Contains(req, "- reviewed: approved skill") {
		t.Errorf("skills_list missed the approved skill")
	}
	view := e.RequestText(t, 2)
	if !strings.Contains(view, `<untrusted source=\"skill:reviewed\">`) || !strings.Contains(view, body) {
		t.Errorf("skill_view not wrapped as untrusted")
	}
}

// TestSkillViewQuarantined (SEC16-1 negative control): a quarantined skill
// that the user never approved is invisible to the real binary: skills_list
// does not offer it and skill_view refuses it.
func TestSkillViewQuarantined(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "skills_list", `{}`),
		ToolCallReply("c2", "skill_view", `{"name":"pending"}`),
		providertest.Text("done"),
	)
	seedQuarantined(t, e, "pending", "not approved", "PENDING-BODY")
	res := e.Run("list and view a quarantined skill")
	res.RequireOK(t)
	req := e.RequestText(t, 1)
	if !strings.Contains(req, "0 skills:") || strings.Contains(req, "- pending:") {
		t.Errorf("skills_list did not exclude the quarantined skill:\n%s", req)
	}
	if view := e.RequestText(t, 2); strings.Contains(view, "PENDING-BODY") || !strings.Contains(view, "not approved or does not exist") {
		t.Errorf("skill_view leaked a quarantined skill:\n%s", view)
	}
}

// seedApproved writes and indexes one trusted skill so the child process can
// list and view it. The fixture lives in the test file (not a production
// package) and uses the storage owners' APIs instead of raw SQL.
func seedApproved(t *testing.T, e *Env, name, description, body string) {
	t.Helper()
	ctx := t.Context()
	dir := filepath.Join(e.ConfigDir(), "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
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
		Name: name, Description: description,
		Body: body, Path: path, SHA256: hex.EncodeToString(sum[:]), Source: "user",
	}, 0); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// seedQuarantined writes and quarantines one agent skill that is never
// approved, using the same storage API the manager uses.
func seedQuarantined(t *testing.T, e *Env, name, description, body string) {
	t.Helper()
	ctx := t.Context()
	dir := filepath.Join(e.DataDir, "skills", "agent", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(md))
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := skillstore.Quarantine(ctx, db, &skillstore.Row{
		Name: name, Description: description,
		Body: body, Path: path, SHA256: hex.EncodeToString(sum[:]), Source: "agent",
	}, "{}", false, 0); err != nil {
		t.Fatalf("seed quarantined skill: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
