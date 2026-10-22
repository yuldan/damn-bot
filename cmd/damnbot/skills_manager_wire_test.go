package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/tools"
)

type skillTestEnv struct {
	db        *ctxdb.Store
	mgr       *skillManager
	dataDir   string
	configDir string
}

func newSkillTestEnv(t *testing.T) *skillTestEnv {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	db, dbErr := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "run"})
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
	return &skillTestEnv{db: db, mgr: newSkillManager(db), dataDir: dataDir, configDir: configDir}
}

func TestSkillReadPath(t *testing.T) {
	t.Parallel()
	env := newSkillTestEnv(t)
	ctx := t.Context()
	if list, err := env.mgr.ListSkills(ctx, 10); err != nil || len(list) != 0 {
		t.Fatalf("initial list = %+v, %v", list, err)
	}
	testSkillQuarantineHidden(ctx, t, env)
	testSkillExactLookup(ctx, t, env)
	testSkillViewCap(ctx, t, env)
}

// testSkillQuarantineHidden is the negative control of the read path: a
// skill that was quarantined but never approved must not be listed by
// skills_list, must not be returned by skill_view, and its files must stay
// untouched.
func testSkillQuarantineHidden(ctx context.Context, t *testing.T, env *skillTestEnv) {
	t.Helper()
	dir := filepath.Join(env.dataDir, "skills", "agent", "pending")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: pending\ndescription: not approved\n---\nbody"
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(md))
	memoryID, _, err := skillstore.Quarantine(ctx, env.db, &skillstore.Row{
		Name: "pending", Description: "not approved", Body: "body", Path: p,
		SHA256: hex.EncodeToString(sum[:]), Source: "agent",
	}, "{}", false, 1)
	if err != nil {
		t.Fatalf("quarantine seed: %v", err)
	}
	if memoryID == 0 {
		t.Fatal("quarantine seed returned no memory row")
	}
	if list, lerr := env.mgr.ListSkills(ctx, 10); lerr != nil || len(list) != 0 {
		t.Fatalf("quarantined skill visible in list: %+v, %v", list, lerr)
	}
	if _, verr := env.mgr.ViewSkill(ctx, "pending"); !errors.Is(verr, tools.ErrSkillNotFound) {
		t.Fatalf("quarantined view = %v, want not_found", verr)
	}
	if _, serr := os.Stat(p); serr != nil {
		t.Fatalf("read path removed a quarantined file: %v", serr)
	}
}

func testSkillExactLookup(ctx context.Context, t *testing.T, env *skillTestEnv) {
	t.Helper()
	for i := range 51 {
		name := fmt.Sprintf("similar-%02d", i)
		dir := filepath.Join(env.configDir, "skills", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		md := "---\nname: " + name + "\ndescription: similar\n---\nbody"
		p := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(p, []byte(md), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(md))
		_, err := skillstore.Index(ctx, env.db, &skillstore.Row{
			Name: name, Description: "similar",
			Body: "body", Path: p, SHA256: hex.EncodeToString(sum[:]), Source: "user",
		}, 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	if doc, err := env.mgr.ViewSkill(ctx, "similar-50"); err != nil || doc.Name != "similar-50" {
		t.Fatalf("exact lookup among >50 rows = %+v, %v", doc, err)
	}
	if list, err := env.mgr.ListSkills(ctx, 100); err != nil || len(list) != 50 {
		t.Fatalf("skills_list cap = %d rows, %v", len(list), err)
	}
}

func testSkillViewCap(ctx context.Context, t *testing.T, env *skillTestEnv) {
	name := "capped"
	dir := filepath.Join(env.configDir, "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: capped\ndescription: caps\n---\nbody"
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(strings.Repeat("x", 40<<10)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte(md))
	_, err := skillstore.Index(ctx, env.db, &skillstore.Row{
		Name: name, Description: "caps",
		Body: "body", Path: p, SHA256: hex.EncodeToString(sum[:]), Source: "user",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := env.mgr.ViewSkill(ctx, name)
	if err != nil || !doc.Truncated {
		t.Fatalf("capped view = truncated %v, %v", doc.Truncated, err)
	}
	total := 0
	for _, f := range doc.Files {
		total += len(f.Content)
	}
	if total > 64<<10 {
		t.Fatalf("view payload %d > 64 KiB", total)
	}
}
