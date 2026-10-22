package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeSearch struct {
	hits  []SessionHit
	query string
	k     int
	cwd   string
	err   error
}

func (f *fakeSearch) Search(_ context.Context, query string, k int, cwd string) ([]SessionHit, error) {
	f.query, f.k, f.cwd = query, k, cwd
	return f.hits, f.err
}

// TestSessionSearchTool: hits render as untrusted transcript snippets and k
// is clamped to 1..10.
func TestSessionSearchTool(t *testing.T) {
	t.Parallel()
	be := &fakeSearch{hits: []SessionHit{
		{SessionID: "S1", EventID: 4, Kind: "user_message", Snippet: "deploy on friday"},
		{SessionID: "S2", EventID: 9, Kind: "assistant_message", Snippet: "rollback plan"},
	}}
	tool := NewSessionSearch(be)
	res, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"query":"deploy","k":50}`))
	if err != nil || res.Err != nil {
		t.Fatalf("run = %v / %v", err, res.Err)
	}
	if be.query != "deploy" || be.k != 10 || be.cwd != "/w" {
		t.Fatalf("backend query/k/cwd = %q/%d/%q", be.query, be.k, be.cwd)
	}
	if !strings.Contains(res.Content, "S1#4 (user_message): deploy on friday") || !strings.Contains(res.Content, "S2#9") {
		t.Errorf("content = %q", res.Content)
	}
	if res.UntrustedSource != "session" {
		t.Errorf("untrusted source = %q", res.UntrustedSource)
	}
	if tool.Kind() != KindRead || tool.Effect(nil) != EffectRead {
		t.Errorf("kind/effect = %v/%v", tool.Kind(), tool.Effect(nil))
	}
}

// TestSessionSearchToolEmptyAndErrors: no hits is a successful empty result;
// bad input and backend failures keep the closed codes. Without a workspace the
// tool fails closed before reaching the backend (SEC17-1).
func TestSessionSearchToolEmptyAndErrors(t *testing.T) {
	t.Parallel()
	be := &fakeSearch{}
	tool := NewSessionSearch(be)
	res, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"query":"nothing"}`))
	if err != nil || res.Content != "no matching sessions" {
		t.Fatalf("empty result = %+v / %v", res, err)
	}
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"query":""}`)); err == nil || asToolError(t, err).Code != CodeInvalidArgs {
		t.Errorf("empty query = %v", err)
	}
	if _, err := tool.Run(context.Background(), Env{}, []byte(`{"query":"x"}`)); err == nil || asToolError(t, err).Code != CodeInvalidArgs {
		t.Errorf("no workspace = %v, want invalid_args", err)
	}
	be.err = errors.New("fts")
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"query":"x"}`)); err == nil || asToolError(t, err).Code != CodeInternal {
		t.Errorf("backend error = %v", err)
	}
	be.err = context.DeadlineExceeded
	if _, err := tool.Run(context.Background(), Env{Cwd: "/w"}, []byte(`{"query":"x"}`)); err == nil || asToolError(t, err).Code != CodeTimeout {
		t.Errorf("timeout = %v", err)
	}
}

// TestNewToolsSchemaCompiles: both tools fit the eager registry contract
// (name, ≤160-char description, closed schema).
func TestNewToolsSchemaCompiles(t *testing.T) {
	t.Parallel()
	reg, err := NewRegistry(NewMemory(&fakeMemory{}), NewSessionSearch(&fakeSearch{}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, raw := range reg.Schemas() {
		var doc SchemaDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		names = append(names, doc.Name)
	}
	if len(names) != 2 || names[0] != "memory" || names[1] != "session_search" {
		t.Fatalf("names = %v", names)
	}
}
