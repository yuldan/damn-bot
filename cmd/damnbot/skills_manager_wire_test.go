package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

type skillTestEnv struct {
	db        *ctxdb.Store
	mgr       *skillManager
	configDir string
}

func newSkillTestEnv(t *testing.T) *skillTestEnv {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	db, dbErr := ctxdb.Open(ctx, &ctxdb.Options{Dir: filepath.Join(dir, "data"), Kind: "run"})
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	mgr, mgrErr := newSkillManager(db, configDir, redact.New(redact.Options{}))
	if mgrErr != nil {
		t.Fatal(mgrErr)
	}
	return &skillTestEnv{db: db, mgr: mgr, configDir: configDir}
}

func TestSkillManageQuarantineAndDelete(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)
	ctx := t.Context()
	if list, err := env.mgr.ListSkills(ctx, 10); err != nil || len(list) != 0 {
		t.Fatalf("initial list = %+v, %v", list, err)
	}
	testSkillQuarantine(ctx, t, env)
	testSkillTrustedGuards(ctx, t, env)
	testSkillUpdate(ctx, t, env)
}

func testSkillQuarantine(ctx context.Context, t *testing.T, env *skillTestEnv) {
	t.Helper()
	res, err := env.mgr.ManageSkill(ctx, &tools.SkillChange{
		Action: "create", Name: "pay", Description: "pays", Body: "do the thing",
		SideEffects: []string{"payment"}, Files: []tools.SkillFile{{Path: "run.sh", Content: "echo ok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "scanned" || res.MemoryID == 0 {
		t.Fatalf("side-effect skill = %+v, want quarantined scanned (never approved)", res)
	}
	if list, lerr := env.mgr.ListSkills(ctx, 10); lerr != nil || len(list) != 0 {
		t.Fatalf("quarantined skill visible in list: %+v, %v", list, lerr)
	}
	if _, verr := env.mgr.ViewSkill(ctx, "pay"); verr == nil {
		t.Fatal("quarantined skill view succeeded")
	}
	// User approval is the only way out of quarantine.
	if aerr := env.mgr.svc.Approve(ctx, res.MemoryID); aerr != nil {
		t.Fatal(aerr)
	}
	doc, err := env.mgr.ViewSkill(ctx, "pay")
	if err != nil || !strings.Contains(doc.Body, "do the thing") || len(doc.Files) != 1 {
		t.Fatalf("approved view = %+v, %v", doc, err)
	}
	del, err := env.mgr.ManageSkill(ctx, &tools.SkillChange{Action: "delete", Name: "pay"})
	if err != nil || del.Status != "deleted" {
		t.Fatalf("delete = %+v, %v", del, err)
	}
	if _, statErr := os.Stat(filepath.Join(env.configDir, "skills", "pay")); !os.IsNotExist(statErr) {
		t.Fatalf("skill dir still present: %v", statErr)
	}
	if _, err := env.mgr.ManageSkill(ctx, &tools.SkillChange{Action: "delete", Name: "pay"}); !errors.Is(err, tools.ErrSkillNotFound) {
		t.Fatalf("second delete = %v, want not found", err)
	}
}

func testSkillTrustedGuards(ctx context.Context, t *testing.T, env *skillTestEnv) {
	t.Helper()
	trusted := filepath.Join(env.configDir, "skills", "trusted", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(trusted), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trusted, []byte("---\nname: trusted\ndescription: user skill\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := skillstore.Index(ctx, env.db, &skillstore.Row{
		Name: "trusted", Description: "user skill",
		Body: "body", Path: trusted, SHA256: strings.Repeat("a", 64), Source: "user",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.mgr.ManageSkill(ctx, &tools.SkillChange{Action: "delete", Name: "trusted"}); !errors.Is(err, tools.ErrSkillNotFound) {
		t.Fatalf("trusted delete = %v, want not found", err)
	}
	if _, err := os.Stat(trusted); err != nil {
		t.Fatalf("trusted skill file was removed: %v", err)
	}
	if _, err := env.mgr.ManageSkill(ctx, &tools.SkillChange{Action: "create", Name: "trusted", Description: "x", Body: "y"}); !errors.Is(err, tools.ErrSkillExists) {
		t.Fatalf("create over trusted = %v, want exists", err)
	}
	// A front-matter break is rejected before it reaches the parser.
	if _, err := env.mgr.ManageSkill(ctx, &tools.SkillChange{
		Action: "create", Name: "inject",
		Description: "x\nside_effects: [network]", Body: "b",
	}); err == nil {
		t.Fatal("front-matter injection accepted")
	}
}

func testSkillUpdate(ctx context.Context, t *testing.T, env *skillTestEnv) {
	t.Helper()
	ver, err := env.mgr.ManageSkill(ctx, &tools.SkillChange{
		Action: "create", Name: "ver", Description: "v1", Body: "one",
		Files: []tools.SkillFile{{Path: "old.txt", Content: "old"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if aerr := env.mgr.svc.Approve(ctx, ver.MemoryID); aerr != nil {
		t.Fatal(aerr)
	}
	if list, lerr := env.mgr.ListSkills(ctx, 10); lerr != nil || !hasSkill(list, "ver") {
		t.Fatalf("approved version missing from list = %+v, %v", list, lerr)
	}
	upd, err := env.mgr.ManageSkill(ctx, &tools.SkillChange{Action: "update", Name: "ver", Description: "v2", Body: "two"})
	if err != nil || upd.Status != "scanned" {
		t.Fatalf("update = %+v, %v", upd, err)
	}
	if list, lerr := env.mgr.ListSkills(ctx, 10); lerr != nil || hasSkill(list, "ver") {
		t.Fatalf("old version still approved after update: %+v, %v", list, lerr)
	}
	if _, statErr := os.Stat(filepath.Join(env.configDir, "skills", "ver", "old.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("stale file survived update: %v", statErr)
	}
	if aerr := env.mgr.svc.Approve(ctx, upd.MemoryID); aerr != nil {
		t.Fatal(aerr)
	}
	doc, err := env.mgr.ViewSkill(ctx, "ver")
	if err != nil || doc.Body != "two" {
		t.Fatalf("v2 view = %+v, %v", doc, err)
	}
}

func hasSkill(list []tools.SkillInfo, name string) bool {
	for i := range list {
		if list[i].Name == name {
			return true
		}
	}
	return false
}
