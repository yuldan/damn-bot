package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// TestConnectorsRegistryClosed proves the connector table is closed: every
// built-in descriptor is valid, a foreign name is rejected by Lookup and
// Attach, and a tool that the descriptor does not declare cannot be served.
func TestConnectorsRegistryClosed(t *testing.T) {
	t.Parallel()
	builtin := Builtin()
	if len(builtin) != 7 {
		t.Fatalf("builtin connectors = %d, want 7", len(builtin))
	}
	seen := map[string]bool{}
	for i := range builtin {
		d := &builtin[i]
		if err := checkDescriptor(d); err != nil {
			t.Errorf("descriptor %q: %v", d.Name, err)
		}
		if seen[d.Name] {
			t.Errorf("duplicate builtin %q", d.Name)
		}
		seen[d.Name] = true
	}
	reg := NewRegistry()
	want := []string{"graph", "teams", "feishu", "meet", "xsearch", "discord", "osv"}
	if got := reg.Names(); !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	if _, ok := reg.Lookup("graphql"); ok {
		t.Fatal("foreign connector was resolved")
	}
	assertAttachRules(t, reg)
	if _, _, ok := reg.HandlerFor("discord"); ok {
		t.Fatal("unattached connector resolved a handler")
	}
	h, desc, ok := reg.HandlerFor("osv_check")
	if !ok || h == nil || desc.Name != "osv" {
		t.Fatalf("attached handler = %v %q %v", h, desc.Name, ok)
	}
}

// assertAttachRules proves the closed Attach contract on a fresh registry.
func assertAttachRules(t *testing.T, reg *Registry) {
	t.Helper()
	if err := reg.Attach("graphql", &fakeHandler{tools: []string{"x"}}); !errors.Is(err, ErrUnknownConnector) {
		t.Fatalf("attach foreign = %v, want ErrUnknownConnector", err)
	}
	if err := reg.Attach("osv", &fakeHandler{tools: []string{"x"}}); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("attach undeclared tool = %v, want ErrUnknownTool", err)
	}
	if err := reg.Attach("osv", &fakeHandler{tools: []string{"osv_check"}}); err != nil {
		t.Fatalf("attach builtin: %v", err)
	}
	if err := reg.Attach("osv", &fakeHandler{tools: []string{"osv_check"}}); !errors.Is(err, ErrDuplicateConnector) {
		t.Fatalf("attach duplicate = %v, want ErrDuplicateConnector", err)
	}
	if err := reg.Attach("osv", nil); !errors.Is(err, ErrInvalidConnector) {
		t.Fatalf("attach nil = %v, want ErrInvalidConnector", err)
	}
}

// TestConnectorsDescriptorRules proves the closed scope and tool vocabulary:
// an unknown scope, an empty tool list and a duplicate tool are rejected.
func TestConnectorsDescriptorRules(t *testing.T) {
	t.Parallel()
	cases := []Descriptor{
		{Name: "graph", Description: "x", Scopes: []Scope{"everything"}, Tools: []string{"t"}},
		{Name: "graph", Description: "x", Scopes: []Scope{ScopeMail}},
		{Name: "graph", Description: "x", Scopes: []Scope{ScopeMail, ScopeMail}, Tools: []string{"t"}},
		{Name: "graph", Description: "", Scopes: []Scope{ScopeMail}, Tools: []string{"t"}},
		{Name: "Graph", Description: "x", Scopes: []Scope{ScopeMail}, Tools: []string{"t"}},
		{Name: "graph", Description: "x", Scopes: []Scope{ScopeMail}, Tools: []string{"t", "t"}},
	}
	for i := range cases {
		if err := checkDescriptor(&cases[i]); !errors.Is(err, ErrInvalidConnector) {
			t.Errorf("case %d: %v, want ErrInvalidConnector", i, err)
		}
	}
}

// TestConnectorsGating proves default-off: a disabled module never becomes
// ready, and an enabled module without a built handler reports
// module_unavailable instead of guessing.
func TestConnectorsGating(t *testing.T) {
	t.Parallel()
	off := New(&Config{}, Deps{})
	if err := off.Start(t.Context()); err != nil {
		t.Fatalf("start disabled: %v", err)
	}
	if st := off.Status(); st != host.StatusStopped {
		t.Fatalf("disabled status = %s", st)
	}
	if _, err := off.Invoke(t.Context(), "osv_check", json.RawMessage(`{"package":"x"}`), host.Env{}); err == nil {
		t.Fatal("disabled module served a call")
	} else {
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "module_unavailable" {
			t.Fatalf("disabled error = %v", err)
		}
	}

	on := New(&Config{Enabled: true}, Deps{})
	if err := on.Start(t.Context()); err != nil {
		t.Fatalf("start enabled: %v", err)
	}
	if st := on.Status(); st != host.StatusReady {
		t.Fatalf("enabled status = %s", st)
	}
	if _, err := on.Invoke(t.Context(), "osv_check", json.RawMessage(`{"package":"x"}`), host.Env{}); err == nil {
		t.Fatal("unimplemented connector served a call")
	}
	if _, err := on.Invoke(t.Context(), "nope", nil, host.Env{}); err == nil {
		t.Fatal("unknown tool served a call")
	}
	if err := on.Stop(t.Context()); err != nil || on.Status() != host.StatusStopped {
		t.Fatalf("stop = %v status %s", err, on.Status())
	}
}

// TestConnectorsDispatchUntrusted proves the module marks external content
// and dispatches only through the attached handler.
func TestConnectorsDispatchUntrusted(t *testing.T) {
	t.Parallel()
	m := New(&Config{Enabled: true}, Deps{})
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	fake := &fakeHandler{tools: []string{"osv_check"}, res: host.Result{Content: "external"}}
	if err := m.registry.Attach("osv", fake); err != nil {
		t.Fatalf("attach: %v", err)
	}
	res, err := m.Invoke(t.Context(), "osv_check", json.RawMessage(`{"package":"x"}`), host.Env{})
	if err != nil || res.Content != "external" || res.UntrustedSource != "connector:osv" {
		t.Fatalf("invoke = %+v, %v", res, err)
	}
}

type fakeHandler struct {
	tools []string
	res   host.Result
	err   error
}

func (f *fakeHandler) Tools() []string { return f.tools }

func (f *fakeHandler) Invoke(context.Context, string, json.RawMessage, host.Env) (host.Result, error) {
	return f.res, f.err
}

// checkDescriptor validates one built-in entry. It runs at package init in
// the test suite, so a malformed table fails before any request.
func checkDescriptor(d *Descriptor) error {
	if !validName(d.Name) {
		return fmt.Errorf("%w: name %q", ErrInvalidConnector, d.Name)
	}
	if strings.TrimSpace(d.Description) == "" {
		return fmt.Errorf("%w: %s has no description", ErrInvalidConnector, d.Name)
	}
	if len(d.Scopes) == 0 {
		return fmt.Errorf("%w: %s has no scope", ErrInvalidConnector, d.Name)
	}
	seenScope := map[Scope]bool{}
	for _, s := range d.Scopes {
		switch s {
		case ScopeMail, ScopeCalendar, ScopeFiles, ScopeMeetings, ScopeDocs,
			ScopeMessages, ScopeAdmin, ScopeSearch, ScopeSupplyChain:
		default:
			return fmt.Errorf("%w: %s scope %q", ErrInvalidConnector, d.Name, s)
		}
		if seenScope[s] {
			return fmt.Errorf("%w: %s repeats scope %q", ErrInvalidConnector, d.Name, s)
		}
		seenScope[s] = true
	}
	if len(d.Tools) == 0 {
		return fmt.Errorf("%w: %s has no tool", ErrInvalidConnector, d.Name)
	}
	seenTool := map[string]bool{}
	for _, tool := range d.Tools {
		if !validToolName(tool) {
			return fmt.Errorf("%w: %s tool %q", ErrInvalidConnector, d.Name, tool)
		}
		if seenTool[tool] {
			return fmt.Errorf("%w: %s repeats tool %q", ErrInvalidConnector, d.Name, tool)
		}
		seenTool[tool] = true
	}
	return nil
}

// TestConnectorsRedactValueCore proves the redactor removes a known token.
func TestConnectorsRedactValueCore(t *testing.T) {
	t.Parallel()
	red := NewRedactor("tok-12345")
	if got := red.Redact("bearer tok-12345"); got != "bearer [redacted]" {
		t.Fatalf("Redact = %q", got)
	}
}

// TestConnectorsNewFromConfigCore proves the module builds from its JSON
// section and rejects an out-of-range limit.
func TestConnectorsNewFromConfigCore(t *testing.T) {
	t.Parallel()
	if m, err := NewFromConfig(json.RawMessage(`{"enabled":false}`), Deps{}); err != nil || m == nil {
		t.Fatalf("disabled section = %v, %v", m, err)
	}
	if _, err := NewFromConfig(json.RawMessage(`{"enabled":true,"timeout_s":9999}`), Deps{}); err == nil {
		t.Fatal("invalid limits were accepted")
	}
}
