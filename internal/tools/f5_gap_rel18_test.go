package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// TestGAPF5_REL18 pins the fail-closed half of the session-view contract:
// a missing or empty view is an internal error, never another session's set;
// an explicit empty capability set denies the user-memory scope.
func TestGAPF5_REL18(t *testing.T) {
	t.Parallel()
	be := &fakeMemory{addID: 1, status: "scanned"}
	tool := NewMemory(be)
	args := json.RawMessage(`{"action":"add","target":"user","content":"x"}`)
	if _, err := tool.Run(context.Background(), Env{}, args); err == nil || asToolError(t, err).Code != CodeInternal {
		t.Fatalf("nil session view = %v, want internal", err)
	}
	noCap, err := NewSessionView("B", "", "run", "/w", []string{"memory"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := tool.Run(context.Background(), Env{Session: noCap}, args); rerr == nil || asToolError(t, rerr).Code != CodeDenied {
		t.Fatalf("missing CapUserMemory = %v, want denied", rerr)
	}
	withCap, err := NewSessionView("A", "", "run", "/w", []string{"memory"}, []Capability{CapUserMemory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), Env{Session: withCap, CallID: "c1"}, args); err != nil {
		t.Fatalf("CapUserMemory call: %v", err)
	}
	if be.addReq.Scope != "user" {
		t.Fatalf("scope = %q, want user", be.addReq.Scope)
	}
	// The session-scoped search tool has the same fail-closed prefix.
	search := NewSessionSearch(&fakeSearch{})
	if _, err := search.Run(context.Background(), Env{}, []byte(`{"query":"x"}`)); err == nil || asToolError(t, err).Code != CodeInternal {
		t.Fatalf("session_search nil view = %v, want internal", err)
	}
}
