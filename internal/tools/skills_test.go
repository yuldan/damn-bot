package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeSkills struct {
	list    []SkillInfo
	doc     SkillDoc
	managed []SkillChange
}

func (f *fakeSkills) ListSkills(context.Context, int) ([]SkillInfo, error) { return f.list, nil }
func (f *fakeSkills) ViewSkill(_ context.Context, name string) (SkillDoc, error) {
	if f.doc.Name != name {
		return SkillDoc{}, ErrSkillNotFound
	}
	return f.doc, nil
}

func (f *fakeSkills) ManageSkill(_ context.Context, c *SkillChange) (SkillResult, error) {
	f.managed = append(f.managed, *c)
	return SkillResult{Action: c.Action, Name: c.Name, Status: "scanned", MemoryID: 4}, nil
}

func TestSkillsList(t *testing.T) {
	t.Parallel()
	f := &fakeSkills{list: []SkillInfo{{Name: "a", Description: "first"}}}
	res, err := NewSkillsList(f).Run(context.Background(), Env{}, json.RawMessage(`{}`))
	if err != nil || !res.OK || !strings.Contains(res.Content, "- a: first") {
		t.Fatalf("list = %+v, %v", res, err)
	}
	if res.UntrustedSource != "" {
		t.Fatalf("metadata is context, not wrapped data: %+v", res)
	}
}

func TestSkillViewUntrusted(t *testing.T) {
	t.Parallel()
	f := &fakeSkills{doc: SkillDoc{
		Name: "a", Description: "d", Body: "body",
		Files: []SkillFile{{Path: "ref.md", Content: "file"}},
	}}
	res, err := NewSkillView(f).Run(context.Background(), Env{}, json.RawMessage(`{"name":"a"}`))
	if err != nil || !res.OK {
		t.Fatalf("view = %+v, %v", res, err)
	}
	if res.UntrustedSource != "skill:a" || !strings.Contains(res.Content, "ref.md") {
		t.Fatalf("view result = %+v", res)
	}
	f.doc.Truncated = true
	res, err = NewSkillView(f).Run(context.Background(), Env{}, json.RawMessage(`{"name":"a"}`))
	if err != nil || !strings.Contains(res.Content, "[skill_view truncated") {
		t.Fatalf("truncated view = %+v, %v", res, err)
	}
	_, err = NewSkillView(f).Run(context.Background(), Env{}, json.RawMessage(`{"name":"missing"}`))
	var te *ToolError
	if !errors.As(err, &te) || te.Code != CodeNotFound {
		t.Fatalf("missing = %v, want not_found", err)
	}
}

func TestSkillManageNeverApproves(t *testing.T) {
	t.Parallel()
	f := &fakeSkills{}
	tool := NewSkillManage(f)
	if tool.Effect(nil) != EffectNonIdempotent {
		t.Fatalf("effect = %s, want non_idempotent", tool.Effect(nil))
	}
	insp, ok := tool.(Inspector)
	if !ok {
		t.Fatal("skill_manage has no inspector")
	}
	for _, mode := range []Mode{ModeAuto, ModeAutoEdit, ModeAsk, ModeHeadlessAuto} {
		dec, derr := insp.Inspect(context.Background(), Env{}, nil, PolicyContext{Mode: mode})
		if derr != nil || dec.Requirement != RequireAsk || dec.AllowSession {
			t.Fatalf("inspect %v = %+v, %v; want always ask", mode, dec, derr)
		}
	}
	args := json.RawMessage(`{"action":"create","name":"new","description":"d","body":"b","side_effects":["payment"]}`)
	res, err := tool.Run(context.Background(), Env{}, args)
	if err != nil || !res.OK || !strings.Contains(res.Content, "scanned") {
		t.Fatalf("manage = %+v, %v", res, err)
	}
	if len(f.managed) != 1 || len(f.managed[0].SideEffects) != 1 || f.managed[0].SideEffects[0] != "payment" {
		t.Fatalf("manager got %+v", f.managed)
	}
	if _, err := tool.Run(context.Background(), Env{}, json.RawMessage(`{"action":"explode","name":"x"}`)); err == nil {
		t.Fatal("bad action succeeded")
	}
}
