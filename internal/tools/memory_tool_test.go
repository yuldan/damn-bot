package tools

import (
	"context"
	"encoding/json"
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
	res, err := tool.Run(context.Background(), Env{Cwd: "/w", CallID: "c1", Session: testView(t, "S")}, []byte(`{"action":"add","target":"user","content":"likes tabs"}`))
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
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w", CallID: "c2", Session: testView(t, "S")}, []byte(`{"action":"replace","target":"memory","id":4,"content":"new"}`)); err != nil {
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
	res, err := tool.Run(context.Background(), Env{Cwd: "/w", Session: testView(t, "S")}, []byte(`{"action":"read","target":"user","query":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "[1] (fact pinned) a") || !strings.Contains(res.Content, "[2] (preference) b") {
		t.Errorf("read content = %q", res.Content)
	}
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w", Session: testView(t, "S")}, []byte(`{"action":"remove"}`)); err == nil {
		t.Error("remove without id accepted")
	} else if terr := asToolError(t, err); terr.Code != CodeInvalidArgs {
		t.Errorf("remove code = %s", terr.Code)
	}
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w", Session: testView(t, "S")}, []byte(`{"action":"remove","id":3}`)); err != nil {
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
	assertToolCode(t, tool, Env{Cwd: "/w", Session: testView(t, "S")}, `{"action":"remove","id":9}`, CodeNotFound)
	assertToolCode(t, tool, Env{}, `{"action":"add","content":"x"}`, CodeInternal)
	assertToolCode(t, tool, Env{Cwd: "/w", Session: testView(t, "S")}, `{"action":"add"}`, CodeInvalidArgs)
	long, merr := json.Marshal(map[string]string{"action": "add", "content": strings.Repeat("x", 501)})
	if merr != nil {
		t.Fatalf("marshal oversized args: %v", merr)
	}
	assertToolCode(t, tool, Env{Cwd: "/w", Session: testView(t, "S")}, string(long), CodeTooLarge)
	assertToolCode(t, tool, Env{Cwd: "/w", Session: testView(t, "S")}, `{"action":"explode"}`, CodeInvalidArgs)
	be.err = ErrMemoryFull
	assertToolCode(t, tool, Env{Cwd: "/w", Session: testView(t, "S")}, `{"action":"add","content":"x"}`, CodeTooLarge)
	be.err = ErrMemoryInvalid
	assertToolCode(t, tool, Env{Cwd: "/w", Session: testView(t, "S")}, `{"action":"add","content":"x"}`, CodeInvalidArgs)
	be.err = ErrMemoryDenied
	assertToolCode(t, tool, Env{Cwd: "/w", Session: testView(t, "S")}, `{"action":"remove","id":9}`, CodeDenied)
	be.err = errors.New("disk")
	assertToolCode(t, tool, Env{Cwd: "/w", Session: testView(t, "S")}, `{"action":"add","content":"x"}`, CodeInternal)
}

// assertToolCode runs the tool and asserts the closed code of the failure.
func assertToolCode(t *testing.T, tool Tool, env Env, args string, want Code) {
	t.Helper()
	if _, err := tool.Run(context.Background(), env, []byte(args)); err == nil || asToolError(t, err).Code != want {
		t.Errorf("run(%s) = %v, want code %s", args, err, want)
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
