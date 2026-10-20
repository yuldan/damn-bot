package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// stubTool is the shared registry test double: a fixed schema and injectable
// Run/Inspect behaviour.
type stubTool struct {
	name   string
	schema string
	kind   Kind
	effect Effect
	run    func(context.Context, Env, json.RawMessage) (Result, error)
	in     func(context.Context, Env, json.RawMessage, PolicyContext) (PolicyDecision, error)

	mu    sync.Mutex
	calls int
	last  Env
}

func (s *stubTool) Name() string                  { return s.name }
func (s *stubTool) Schema() json.RawMessage       { return json.RawMessage(s.schema) }
func (s *stubTool) Kind() Kind                    { return s.kind }
func (s *stubTool) Effect(json.RawMessage) Effect { return s.effect }
func (s *stubTool) Inspect(ctx context.Context, env Env, args json.RawMessage, pc PolicyContext) (PolicyDecision, error) {
	if s.in != nil {
		return s.in(ctx, env, args, pc)
	}
	return defaultPolicy(s.effect, pc), nil
}

func (s *stubTool) Run(ctx context.Context, env Env, args json.RawMessage) (Result, error) {
	s.mu.Lock()
	s.calls++
	s.last = env
	s.mu.Unlock()
	if s.run != nil {
		return s.run(ctx, env, args)
	}
	return Result{OK: true, Content: "ok"}, nil
}

func (s *stubTool) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func echoSchema(name string) string {
	return `{"name":"` + name + `","description":"stub","parameters":{"type":"object","properties":{"body":{"type":"string","maxLength":4096}},"required":["body"],"additionalProperties":false}}`
}

func mustReg(tb testing.TB, list ...Tool) *Registry {
	tb.Helper()
	r, err := NewRegistry(list...)
	if err != nil {
		tb.Fatal(err)
	}
	return r
}

func TestRegistry(t *testing.T) {
	t.Parallel()
	b := &stubTool{name: "b", schema: `{"name":"b","description":"b","parameters":{"type":"object"}}`, effect: EffectRead}
	a := &stubTool{name: "a", schema: `{"name":"a","description":"a","parameters":{"type":"object"}}`, effect: EffectRead}
	r, err := NewRegistry(b, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Names(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("names = %v, want [a b]", got)
	}
	if _, found := r.Lookup("a"); !found {
		t.Fatal("lookup a failed")
	}
	if _, found := r.Lookup("zz"); found {
		t.Fatal("lookup zz succeeded")
	}
	if len(r.Schemas()) != 2 || !strings.Contains(string(r.Schemas()[0]), `"a"`) {
		t.Fatalf("schemas = %s", r.Schemas())
	}
}

func TestRegistryRejects(t *testing.T) {
	t.Parallel()
	good := `{"name":"x","description":"x","parameters":{"type":"object"}}`
	cases := []struct {
		name string
		tool Tool
	}{
		{"nil", nil},
		{"empty", &stubTool{name: "", schema: good}},
		{"name mismatch", &stubTool{name: "x", schema: `{"name":"y","description":"x","parameters":{"type":"object"}}`}},
		{"long description", &stubTool{name: "x", schema: `{"name":"x","description":"` + strings.Repeat("d", 161) + `","parameters":{"type":"object"}}`}},
		{"no parameters", &stubTool{name: "x", schema: `{"name":"x","description":"x"}`}},
		{"bad schema", &stubTool{name: "x", schema: `{"name":"x","description":"x","parameters":{"type":"object","unknown":1}}`}},
		{"bad json", &stubTool{name: "x", schema: `{`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewRegistry(c.tool); err == nil {
				t.Fatal("NewRegistry accepted an invalid registration")
			}
		})
	}
}

func TestRegistryDuplicate(t *testing.T) {
	t.Parallel()
	a := &stubTool{name: "x", schema: `{"name":"x","description":"x","parameters":{"type":"object"}}`}
	b := &stubTool{name: "x", schema: `{"name":"x","description":"x","parameters":{"type":"object"}}`}
	if _, err := NewRegistry(a, b); err == nil {
		t.Fatal("duplicate name accepted")
	}
}

func TestErrorCodeSet(t *testing.T) {
	t.Parallel()
	set := CodeSet()
	for _, c := range []Code{CodeUnknownTool, CodeInvalidArgs, CodeDeniedPath, CodeOutsideWorkspace, CodeDenied, CodeTimeout, CodeIdempotencyConflict, CodeInternal} {
		if !set[c] {
			t.Fatalf("%s missing from the closed set", c)
		}
	}
	if len(set) != 37 {
		t.Fatalf("closed set has %d codes, want 37", len(set))
	}
}
