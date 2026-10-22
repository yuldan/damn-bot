package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeSkills struct {
	list []SkillInfo
	doc  SkillDoc
}

func (f *fakeSkills) ListSkills(context.Context, int) ([]SkillInfo, error) { return f.list, nil }
func (f *fakeSkills) ViewSkill(_ context.Context, name string) (SkillDoc, error) {
	if f.doc.Name != name {
		return SkillDoc{}, ErrSkillNotFound
	}
	return f.doc, nil
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
