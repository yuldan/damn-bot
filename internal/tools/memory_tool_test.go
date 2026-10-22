package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeMemory struct {
	addReq    MemoryAdd
	replace   MemoryAdd
	replaceID int64
	removeID  int64
	entries   []MemoryEntry
	addID     int64
	status    string
	err       error
}

func (f *fakeMemory) Add(_ context.Context, req MemoryAdd) (int64, string, error) {
	f.addReq = req
	return f.addID, f.status, f.err
}

func (f *fakeMemory) Replace(_ context.Context, req MemoryAdd, id int64) (int64, string, error) {
	f.replace, f.replaceID = req, id
	return f.addID, f.status, f.err
}

func (f *fakeMemory) Remove(_ context.Context, id int64, _ string) error {
	f.removeID = id
	return f.err
}

func (f *fakeMemory) Read(context.Context, string, string, int) ([]MemoryEntry, error) {
	return f.entries, f.err
}

// TestMemoryToolAdd: an agent write carries the call id as evidence and
// reports the quarantine status.
func TestMemoryToolAdd(t *testing.T) {
	t.Parallel()
	be := &fakeMemory{addID: 7, status: "scanned"}
	tool := NewMemory(be)
	res, err := tool.Run(context.Background(), Env{Cwd: "/w", CallID: "c1"}, []byte(`{"action":"add","target":"user","content":"likes tabs"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Err != nil || !res.OK {
		t.Fatalf("result = %+v", res)
	}
	if be.addReq.CallID != "c1" || be.addReq.Scope != "user" || be.addReq.Category != "fact" || be.addReq.Content != "likes tabs" {
		t.Fatalf("add request = %+v", be.addReq)
	}
	if !strings.Contains(res.Content, "stored memory 7 (scanned)") || !strings.Contains(res.Content, "quarantined") {
		t.Errorf("content = %q", res.Content)
	}
	if tool.Effect(nil) != EffectNonIdempotent || tool.Kind() != KindEdit {
		t.Errorf("kind/effect = %v/%v", tool.Kind(), tool.Effect(nil))
	}
}

// TestMemoryToolReplace: replace supersedes the given id in the same scope.
func TestMemoryToolReplace(t *testing.T) {
	t.Parallel()
	be := &fakeMemory{addID: 8, status: "approved"}
	tool := NewMemory(be)
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w", CallID: "c2"}, []byte(`{"action":"replace","target":"memory","id":4,"content":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if be.replaceID != 4 || be.replace.Scope != "project:/w" || be.replace.Content != "new" {
		t.Fatalf("replace = %+v id %d", be.replace, be.replaceID)
	}
}

// TestMemoryToolReadRemove: read renders entries, remove needs a valid id.
func TestMemoryToolReadRemove(t *testing.T) {
	t.Parallel()
	be := &fakeMemory{entries: []MemoryEntry{{ID: 1, Category: "fact", Scope: "user", Content: "a", Pinned: true}, {ID: 2, Category: "preference", Scope: "user", Content: "b"}}}
	tool := NewMemory(be)
	res, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"read","target":"user","query":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "[1] (fact pinned) a") || !strings.Contains(res.Content, "[2] (preference) b") {
		t.Errorf("read content = %q", res.Content)
	}
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"remove"}`)); err == nil {
		t.Error("remove without id accepted")
	} else if terr := asToolError(t, err); terr.Code != CodeInvalidArgs {
		t.Errorf("remove code = %s", terr.Code)
	}
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"remove","id":3}`)); err != nil {
		t.Fatal(err)
	}
	if be.removeID != 3 {
		t.Errorf("remove id = %d", be.removeID)
	}
}

// TestMemoryToolErrors: backend errors keep the closed code set, and target
// memory needs a workspace.
func TestMemoryToolErrors(t *testing.T) {
	t.Parallel()
	be := &fakeMemory{err: ErrMemoryNotFound}
	tool := NewMemory(be)
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"remove","id":9}`)); err == nil || asToolError(t, err).Code != CodeNotFound {
		t.Errorf("not-found mapping = %v", err)
	}
	if _, err := tool.Run(context.Background(), Env{}, []byte(`{"action":"add","content":"x"}`)); err == nil || asToolError(t, err).Code != CodeInvalidArgs {
		t.Errorf("target memory without cwd = %v", err)
	}
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"add"}`)); err == nil || asToolError(t, err).Code != CodeInvalidArgs {
		t.Errorf("add without content = %v", err)
	}
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"explode"}`)); err == nil || asToolError(t, err).Code != CodeInvalidArgs {
		t.Errorf("unknown action = %v", err)
	}
	be.err = ErrMemoryFull
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"add","content":"x"}`)); err == nil || asToolError(t, err).Code != CodeTooLarge {
		t.Errorf("full mapping = %v", err)
	}
	be.err = ErrMemoryInvalid
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"add","content":"x"}`)); err == nil || asToolError(t, err).Code != CodeInvalidArgs {
		t.Errorf("invalid mapping = %v", err)
	}
	be.err = errors.New("disk")
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"action":"add","content":"x"}`)); err == nil || asToolError(t, err).Code != CodeInternal {
		t.Errorf("internal mapping = %v", err)
	}
}

// asToolError extracts the typed error from a Run failure.
func asToolError(t *testing.T, err error) *ToolError {
	t.Helper()
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want *ToolError", err)
	}
	return te
}
