package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// testView is the explicit view of a test session. It carries the user
// memory capability so the memory tool tests keep exercising the user scope.
func testView(t *testing.T, id string) SessionView {
	t.Helper()
	v, err := NewSessionView(id, "", "run", "/w",
		[]string{"memory", "session_search", "delegate_task", "execute_code"},
		[]Capability{CapUserMemory, CapDelegate})
	if err != nil {
		t.Fatalf("session view: %v", err)
	}
	return v
}

func TestSessionViewFailClosed(t *testing.T) {
	t.Parallel()
	var zero SessionView
	if zero.Valid() || zero.Allows("memory") || zero.Can(CapDelegate) || len(zero.Tools()) != 0 {
		t.Fatal("zero SessionView must allow nothing")
	}
	if _, err := zero.Child("child", nil, nil); err == nil {
		t.Fatal("child of the zero view accepted")
	}
	empty, err := NewSessionView("S", "", "run", "/w", nil, nil)
	if err != nil || !empty.Valid() || empty.Allows("memory") {
		t.Fatalf("explicit empty set = %+v / %v", empty, err)
	}
	if _, err := NewSessionView("", "", "run", "/w", nil, nil); err == nil {
		t.Fatal("empty id accepted")
	}
	if _, err := NewSessionView("S", "", "run", "/w", []string{"a", "a"}, nil); err == nil {
		t.Fatal("duplicate tool accepted")
	}
	if _, err := NewSessionView("S", "", "run", "/w", nil, []Capability{"root"}); err == nil {
		t.Fatal("unknown capability accepted")
	}
}

// TestSessionViewChildCannotEscalate: a child keeps only the intersection of
// its parent's tools and capabilities, increments depth, and never inherits
// approval bypass or secret access (SPEC §9.3).
//
//nolint:gocyclo // one assertion per invariant in the capability-intersection matrix.
func TestSessionViewChildCannotEscalate(t *testing.T) {
	t.Parallel()
	parent, err := NewSessionView("P", "", "gateway", "/w",
		[]string{"read_file", "delegate_task", "shell"},
		[]Capability{CapDelegate, CapNetwork, CapApprovalBypass, CapSecretAccess})
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.Child("C", []string{"read_file", "delegate_task", "write_file", "mcp_echo"},
		[]Capability{CapDelegate, CapNetwork, CapApprovalBypass, CapSecretAccess, CapMCP})
	if err != nil {
		t.Fatal(err)
	}
	if child.Depth() != 1 || child.ParentID() != "P" || child.Origin() != "delegate" {
		t.Fatalf("child identity = %+v", child)
	}
	if got := child.Tools(); len(got) != 2 || got[0] != "delegate_task" || got[1] != "read_file" {
		t.Fatalf("child tools = %v", got)
	}
	if child.Can(CapApprovalBypass) || child.Can(CapSecretAccess) {
		t.Fatal("child inherited approval bypass or secret access")
	}
	if !child.Can(CapDelegate) || !child.Can(CapNetwork) || child.Can(CapMCP) {
		t.Fatalf("child capabilities = %v", child.Capabilities())
	}
	if len(parent.Tools()) != 3 || !parent.Can(CapApprovalBypass) {
		t.Fatal("Child mutated its parent")
	}
}

// probeTool reports the explicit view it was handed. It is the execute_code
// stand-in of TestSessionToolIsolation: the RPC sees only the caller's view.
type probeTool struct{}

func (probeTool) Name() string { return "probe" }
func (probeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"name":"probe","description":"report the caller session view","parameters":{"type":"object","properties":{},"additionalProperties":false}}`)
}
func (probeTool) Kind() Kind                    { return KindExecute }
func (probeTool) Effect(json.RawMessage) Effect { return EffectRead }
func (probeTool) Run(_ context.Context, env Env, _ json.RawMessage) (Result, error) {
	if !env.Session.Valid() {
		return Result{}, toolErr(CodeInternal, "no session view", "")
	}
	return Result{OK: true, Content: fmt.Sprintf("%s|%v|%v", env.Session.SessionID(), env.Session.Tools(), env.Session.Capabilities())}, nil
}

// TestSessionToolIsolation is GAP-REL-18's acceptance: two sessions with
// different tool sets run in parallel 1.000 times each and every RPC sees only
// its own set, including under -race. The zero view is the negative control:
// it fails closed instead of falling back to a global set.
func TestSessionToolIsolation(t *testing.T) {
	t.Parallel()
	viewA, err := NewSessionView("A", "", "gateway", "/a", []string{"probe"}, []Capability{CapExecuteCode})
	if err != nil {
		t.Fatal(err)
	}
	viewB, err := NewSessionView("B", "", "gateway", "/b", []string{"probe", "shell"}, []Capability{CapExecuteCode, CapNetwork})
	if err != nil {
		t.Fatal(err)
	}
	w := newWorkspace(t)
	newDisp := func(view SessionView) *Dispatcher {
		d, _, _ := w.dispatcher(t, ModeAutoEdit, nil, probeTool{})
		d.session = view
		return d
	}
	dA, dB := newDisp(viewA), newDisp(viewB)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	run := func(d *Dispatcher, id string, want string) {
		defer wg.Done()
		for i := range 1000 {
			d.BeginTurn() // one independent turn per RPC, so the inline cap does not spill
			out := d.Dispatch(context.Background(), Call{Name: "probe", Args: json.RawMessage(`{}`)})
			if out.Err != nil {
				errs <- fmt.Errorf("%s iteration %d: %w", id, i, out.Err)
				return
			}
			if out.Content != want {
				errs <- fmt.Errorf("%s iteration %d: saw %q, want %q", id, i, out.Content, want)
				return
			}
		}
	}
	wg.Add(2)
	go run(dA, "A", "A|[probe]|[execute_code]")
	go run(dB, "B", "B|[probe shell]|[execute_code network]")
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// Negative control: an omitted view is an internal error, never another
	// session's set.
	zero := newDisp(SessionView{})
	if out := zero.Dispatch(context.Background(), Call{Name: "probe", Args: json.RawMessage(`{}`)}); out.Err == nil || out.Err.Code != CodeInternal {
		t.Fatalf("zero view = %+v, want internal", out.Err)
	}
}
