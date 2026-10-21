package host

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
)

func TestStatusSet(t *testing.T) {
	t.Parallel()
	all := Statuses()
	if len(all) != 6 {
		t.Fatalf("Statuses = %d, want 6", len(all))
	}
	seen := map[Status]bool{}
	for _, s := range all {
		if !s.Valid() || seen[s] {
			t.Fatalf("status %q invalid or repeated", s)
		}
		seen[s] = true
	}
	if Status("running").Valid() {
		t.Fatal("unknown status accepted")
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"zero", Config{}, true},
		{"prefixed", Config{Key: "modules.browser"}, true},
		{"prefixed with json", Config{Key: "modules.browser", Raw: json.RawMessage(`{"a":1}`)}, true},
		{"bad prefix", Config{Key: "browser"}, false},
		{"whitespace", Config{Key: "modules .browser"}, false},
		{"raw array", Config{Key: "modules.browser", Raw: json.RawMessage(`[1]`)}, false},
		{"raw garbage", Config{Key: "modules.browser", Raw: json.RawMessage(`{`)}, false},
		{"long key", Config{Key: "modules." + strings.Repeat("x", 200)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestSpawnSpecValidate(t *testing.T) {
	t.Parallel()
	base := SpawnSpec{Path: "/usr/bin/true"}
	cases := []struct {
		name string
		spec SpawnSpec
		ok   bool
	}{
		{"minimal", base, true},
		{"args env dir", SpawnSpec{Path: "tool", Args: []string{"-x"}, Dir: "/tmp", Env: []string{"A=1"}}, true},
		{"empty path", SpawnSpec{}, false},
		{"nul path", SpawnSpec{Path: "to\x00ol"}, false},
		{"nul arg", SpawnSpec{Path: "tool", Args: []string{"a\x00"}}, false},
		{"env without value", SpawnSpec{Path: "tool", Env: []string{"A"}}, false},
		{"env empty key", SpawnSpec{Path: "tool", Env: []string{"=1"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.spec.Validate()
			if c.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalidSpawn) {
				t.Fatalf("Validate = %v, want ErrInvalidSpawn", err)
			}
		})
	}
}

func TestEventValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   Event
		ok   bool
	}{
		{"start", Event{Kind: EventModuleStart, Content: "browser"}, true},
		{"error payload", Event{Kind: EventModuleError, Content: "x", Payload: json.RawMessage(`{"a":1}`)}, true},
		{"notice", Event{Kind: EventSystemNotice}, true},
		{"foreign kind", Event{Kind: "assistant_message"}, false},
		{"empty kind", Event{}, false},
		{"long content", Event{Kind: EventModuleStart, Content: strings.Repeat("x", MaxEventText+1)}, false},
		{"array payload", Event{Kind: EventModuleStart, Payload: json.RawMessage(`[]`)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ev.Validate()
			if c.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("Validate = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestResultValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  Result
		ok   bool
	}{
		{"empty", Result{}, true},
		{"content", Result{Content: "hello", Meta: map[string]any{"k": 1}}, true},
		{"untrusted", Result{Content: "x", UntrustedSource: "browser"}, true},
		{"too large", Result{Content: strings.Repeat("x", MaxResultBytes+1)}, false},
		{"bad source", Result{UntrustedSource: `bad"source`}, false},
		{"bad meta key", Result{Meta: map[string]any{"": 1}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.res.Validate()
			if c.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !c.ok && !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("Validate = %v, want ErrInvalidResult", err)
			}
		})
	}
}

func TestErrorValidate(t *testing.T) {
	t.Parallel()
	if err := (*Error)(nil).Validate(); err != nil {
		t.Fatalf("nil error: %v", err)
	}
	if err := (&Error{Code: "internal", Message: "x"}).Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := (&Error{Code: "bad code"}).Validate(); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("code with space = %v", err)
	}
	if got := (&Error{Code: "internal", Message: "boom"}).Error(); !strings.Contains(got, "internal") {
		t.Fatalf("Error() = %q", got)
	}
}

func TestNamespaces(t *testing.T) {
	t.Parallel()
	ns, err := Namespace("gateway.telegram")
	if err != nil || ns != "module/gateway.telegram" {
		t.Fatalf("Namespace = %q, %v", ns, err)
	}
	if err := ValidateNamespace(ns); err != nil {
		t.Fatalf("ValidateNamespace(%q): %v", ns, err)
	}
	for _, bad := range []string{"", "gateway", "module/", "module/../x", "module/UPPER", "module/a b"} {
		if err := ValidateNamespace(bad); !errors.Is(err, ErrInvalidNamespace) {
			t.Errorf("ValidateNamespace(%q) = %v", bad, err)
		}
	}
	if _, err := Namespace("Bad ID"); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("Namespace(Bad ID) = %v", err)
	}
}

// fakeHost proves the Host interface is implementable with the documented
// five methods and no hidden dependencies.
type fakeHost struct {
	events []Event
}

var _ Host = (*fakeHost)(nil)

func (h *fakeHost) Emit(_ context.Context, ev Event) error {
	h.events = append(h.events, ev)
	return nil
}
func (h *fakeHost) I18n() Translator { return fakeTranslator{} }
func (h *fakeHost) Spawn(context.Context, SpawnSpec) (Process, error) {
	return nil, errors.New("unused")
}
func (h *fakeHost) LLM(string) Completer { return fakeCompleter{} }
func (h *fakeHost) Storage(namespace string) (Store, error) {
	if err := ValidateNamespace(namespace); err != nil {
		return nil, err
	}
	return fakeStore{}, nil
}

type fakeTranslator struct{}

func (fakeTranslator) Message(locale, key string, _ ...any) string { return locale + ":" + key }

type fakeCompleter struct{}

func (fakeCompleter) Complete(context.Context, Request) (Response, error) {
	return Response{Content: "ok", Usage: Usage{InputTokens: 1}}, nil
}

type fakeStore struct{}

func (fakeStore) Get(context.Context, []byte) ([]byte, bool, error) { return nil, false, nil }
func (fakeStore) Put(context.Context, []byte, []byte) error         { return nil }
func (fakeStore) Delete(context.Context, []byte) error              { return nil }
func (fakeStore) List(context.Context, []byte) ([][]byte, error)    { return nil, nil }

func TestHostContract(t *testing.T) {
	t.Parallel()
	var h Host = &fakeHost{}
	ctx := context.Background()
	if err := h.Emit(ctx, Event{Kind: EventModuleStart, Content: "browser"}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if got := h.I18n().Message("id", "module.started"); got != "id:module.started" {
		t.Fatalf("I18n = %q", got)
	}
	resp, err := h.LLM("module").Complete(ctx, Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil || resp.Content != "ok" || resp.Usage.InputTokens != 1 {
		t.Fatalf("Complete = %+v, %v", resp, err)
	}
	ns, err := Namespace("browser")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Storage(ns); err != nil {
		t.Fatalf("Storage: %v", err)
	}
	if _, err := h.Storage("../escape"); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("escape namespace = %v", err)
	}
}

// TestHostImports is the E-06 contract: host imports only the standard
// library plus internal/loop/event and internal/schema, so a module that
// depends on it stays out of the core's dependency graph.
func TestHostImports(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{
		"github.com/damnbot/damnbot/internal/loop/event": true,
		"github.com/damnbot/damnbot/internal/schema":     true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !strings.Contains(strings.SplitN(p, "/", 2)[0], ".") || allowed[p] {
				continue
			}
			t.Errorf("E-06 host import %q outside the allowed set", p)
		}
	}
}
