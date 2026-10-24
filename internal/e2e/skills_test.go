package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	e := NewTrusted(t,
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

// TestSkillManageAlwaysAsks (SEC16-2/SEC16-4 negative control): even with
// --approval auto, skill_manage must not write a skill without an approver.
func TestSkillManageAlwaysAsks(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "skill_manage", `{"action":"create","name":"agent-note","description":"d","body":"b"}`),
		providertest.Text("done"),
	)
	res := e.Run("create a skill", "--approval", "auto")
	res.RequireOK(t)
	// The denial is asserted on the structured envelope the loop hands back to
	// the provider (loop.errorEnvelope), not on free text: code is a typed
	// constant, so a wording change cannot make this control pass vacuously.
	env := toolResultEnvelope(t, e, 1, "c1")
	if env.OK {
		t.Fatalf("skill_manage result = %+v, want ok=false", env)
	}
	if env.Error.Code != deniedToolCode {
		t.Fatalf("skill_manage denial code = %q, want %q", env.Error.Code, deniedToolCode)
	}
	for _, p := range []string{
		filepath.Join(e.DataDir, "skills", "agent", "agent-note"),
		filepath.Join(e.ConfigDir(), "skills", "agent-note"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("denied skill wrote %s: %v", p, err)
		}
	}
}

// deniedToolCode is loop.CodeDenied ("denied"): internal/e2e may only import
// internal/loop/event (archtest E-01), so the code is spelled here and the
// envelope shape is what the assertion really checks.
const deniedToolCode = "denied"

// toolEnvelope is the loop's model-facing tool result (loop.errorEnvelope):
// {"ok":…,"error":{"code":…,"message":…,"hint":…}}.
type toolEnvelope struct {
	OK    bool `json:"ok"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

// toolResultEnvelope decodes the tool result the model sees for callID in
// provider request n. It fails when the pairing is missing or is not the loop
// envelope, so a renamed call id cannot silently pass the assertion.
func toolResultEnvelope(t *testing.T, e *Env, n int, callID string) toolEnvelope {
	t.Helper()
	msgs, ok := e.RequestJSON(t, n)["messages"].([]any)
	if !ok {
		t.Fatalf("request %d has no messages array", n)
	}
	for _, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok || m["role"] != "tool" || m["tool_call_id"] != callID {
			continue
		}
		content, isText := m["content"].(string)
		if !isText {
			t.Fatalf("tool result for %s has no text content: %#v", callID, m["content"])
		}
		var env toolEnvelope
		if err := json.Unmarshal([]byte(content), &env); err != nil {
			t.Fatalf("tool result for %s is not the loop envelope: %v\ncontent:\n%s", callID, err, content)
		}
		return env
	}
	t.Fatalf("no tool message for call %s in request %d", callID, n)
	return toolEnvelope{}
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

// tomlLiteral renders s as a TOML literal string, which has no escape
// processing, so a backslash path is written verbatim.
func tomlLiteral(t *testing.T, s string) string {
	t.Helper()
	if strings.ContainsAny(s, "'") {
		t.Fatalf("path %q contains a single quote; literal string cannot carry it", s)
	}
	return "'" + s + "'"
}

// TestImportedSkillQuarantined (negative control through the real binary): a
// skill discovered in a new, untrusted location (skills.external_dirs) is
// quarantined, so skills_list never offers it to the model before the user
// approves it.
func TestImportedSkillQuarantined(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "skills_list", `{}`),
		providertest.Text("done"),
	)
	ext := filepath.Join(t.TempDir(), "ext")
	dir := filepath.Join(ext, "proj-skill")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: proj-skill\ndescription: from the project\n---\nIMPORTED-BODY"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	// The path goes in as a TOML literal string: a Windows path contains
	// backslashes, and inside a basic string "C:\Users" is an invalid \U
	// escape that makes the whole config unparseable (the child then exits 2).
	if err := os.WriteFile(e.cfg, []byte("[skills]\nexternal_dirs = ["+tomlLiteral(t, ext)+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Run("list the available skills")
	res.RequireOK(t)
	req := e.RequestText(t, 1)
	if !strings.Contains(req, "0 skills:") {
		t.Errorf("skills_list did not report an empty approved catalog:\n%s", req)
	}
	if strings.Contains(req, "proj-skill") || strings.Contains(req, "IMPORTED-BODY") {
		t.Errorf("an unapproved imported skill reached the model:\n%s", req)
	}
}

// TestSkillsTrustCLI (CLI-046, ADR-175, positive and negative control through
// the real binary): `skills trust add` makes the project discovered, so its
// skill is loaded and quarantined (one import row, still invisible), while the
// same project without the entry is not discovered at all (no import row).
func TestSkillsTrustCLI(t *testing.T) {
	t.Parallel()
	// One reply per child run below (the fake provider scripts its turns).
	e := New(t, providertest.Text("done"), providertest.Text("done"), providertest.Text("done"))
	dir := filepath.Join(e.Workdir, ".damnbot", "skills", "proj-skill")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: proj-skill\ndescription: from the project\n---\nIMPORTED-BODY"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	imports := func() int {
		t.Helper()
		db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if cerr := db.Close(); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		}()
		n, err := db.Count(t.Context(), "SELECT count(*) FROM skills WHERE source = 'import'")
		if err != nil {
			t.Fatal(err)
		}
		return int(n)
	}
	// Negative control: without a trust entry the project is never discovered.
	e.Run("no tools").RequireOK(t)
	if got := imports(); got != 0 {
		t.Fatalf("untrusted project produced %d import rows, want 0", got)
	}
	e.Exec("skills", "trust", "add", e.Workdir).RequireOK(t)
	e.Run("no tools").RequireOK(t)
	if got := imports(); got != 1 {
		t.Fatalf("trusted project produced %d import rows, want 1 (quarantined)", got)
	}
	// The quarantined import is still invisible to the skill tools.
	list := e.Exec("skills", "trust", "list")
	list.RequireOK(t)
	if !strings.Contains(list.Stdout, filepath.Base(e.Workdir)) {
		t.Fatalf("trust list = %q", list.Stdout)
	}
	e.Exec("skills", "trust", "remove", e.Workdir).RequireOK(t)
	e.Run("no tools").RequireOK(t)
	if got := imports(); got != 1 {
		t.Fatalf("after remove the import count changed to %d, want the earlier 1", got)
	}
}
