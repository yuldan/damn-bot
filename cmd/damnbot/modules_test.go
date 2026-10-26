package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

// TestRunAdvertisesF1Tools: with no module compiled in, run sends exactly
// the eight F1 eager tools — no tool_search/tool_invoke (P1 unchanged).
func TestRunAdvertisesF1Tools(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("hi"))
	var out, errb bytes.Buffer
	if code := run([]string{"run", "-p", "hi", "--model", "openai/gpt-fake", "--base-url", srv.URL, "--data-dir", t.TempDir(), "--output", "json"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(srv.Requests()[0], &req); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range req.Tools {
		names = append(names, tl.Function.Name)
	}
	slices.Sort(names)
	// main advertises the skill tools in the same wiring (skills list/view/manage)
	// and this PR adds the memory/session_search tool pair (ADR-188), so those
	// are the complete declared payload.
	want := []string{"clarify", "edit_file", "glob", "grep", "memory", "read_file", "session_search", "shell", "skill_manage", "skill_view", "skills_list", "todo", "write_file"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

// TestStartRegistryError: a module registry error ends the process with
// exit 1 before any command runs (ADR-119).
func TestStartRegistryError(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := start(errors.New("duplicate module id"), []string{"--version"}, &out, &errb); code != 1 || out.Len() != 0 || !strings.Contains(errb.String(), "module registry: duplicate module id") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}
	if code := start(nil, []string{"--version"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "damnbot") {
		t.Fatalf("frozen ok: exit %d stdout %q", code, out.String())
	}
}

type demoModule struct{}

func (demoModule) Start(context.Context) error { return nil }
func (demoModule) Status() host.Status         { return host.StatusReady }
func (demoModule) Stop(context.Context) error  { return nil }
func (demoModule) Invoke(_ context.Context, tool string, args json.RawMessage, _ host.Env) (host.Result, error) {
	return host.Result{Content: tool + " got " + string(args)}, nil
}

// testHost is the test double of the not-yet-wired module host.
type testHost struct{}

func (testHost) Emit(context.Context, host.Event) error { return nil }

func (testHost) I18n() host.Translator { return nil }

func (testHost) Spawn(context.Context, host.SpawnSpec) (host.Process, error) {
	return nil, errors.New("no spawn in tests")
}

func (testHost) LLM(string) host.Completer { return nil }

func (testHost) Storage(string) (host.Store, error) { return nil, errors.New("no storage in tests") }

func demoRegistry(t *testing.T) *modules.Registry {
	t.Helper()
	reg := modules.NewRegistry()
	err := reg.Register(modules.Descriptor{
		ID: "demo", Group: "demo", Catalog: "- demo: echo text",
		Tools: []modules.ToolDescriptor{{
			Name: "demo_echo", Description: "Echo text back", Effect: "read",
			Schema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		}},
		Factory: func(host.Host, host.Config) (modules.Module, error) { return demoModule{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

type nopJournal struct{}

func (nopJournal) ToolDispatch(context.Context, string, string, string, tools.Effect, [32]byte) error {
	return nil
}

func (nopJournal) ToolResult(context.Context, string, string, string, bool, string, string) error {
	return nil
}

// TestRunModulesWiring: zero module tools wire nothing; module tools
// without a host refuse to start; with a host tool_search finds the tool and
// tool_invoke runs it through the dispatcher pipeline.
func TestRunModulesWiring(t *testing.T) {
	t.Parallel()
	if m, err := newRunModules(modules.NewRegistry(), nil); err != nil || m.deferred != nil || m.runtime != nil || len(m.meta) != 0 {
		t.Fatalf("zero modules = %+v, %v", m, err)
	}
	if _, err := newRunModules(demoRegistry(t), nil); !errors.Is(err, errNoModuleHost) {
		t.Fatalf("no host: %v", err)
	}
	m, err := newRunModules(demoRegistry(t), testHost{})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tools.NewRegistry(m.meta...)
	if err != nil {
		t.Fatal(err)
	}
	disp, err := tools.NewDispatcher(&tools.Config{
		Registry: reg, Journal: nopJournal{}, Spill: ctxSpill{db: modulesTestDB(t)}, Redactor: redact.New(redact.Options{}),
		Mode: tools.ModeHeadlessAuto, WorkspaceTrusted: true, Cwd: t.TempDir(), Deferred: m.deferred,
	})
	if err != nil {
		t.Fatal(err)
	}
	disp.BeginTurn()
	ctx := context.Background()
	if out := disp.Dispatch(ctx, tools.Call{CallID: "s", Name: "tool_search", Args: json.RawMessage(`{"query":"echo"}`)}); out.Err != nil || !strings.Contains(out.Render(), "demo_echo") {
		t.Fatalf("tool_search = %q %+v", out.Render(), out.Err)
	}
	out := disp.Dispatch(ctx, tools.Call{CallID: "i", Name: "tool_invoke", Args: json.RawMessage(`{"name":"demo_echo","arguments":{"text":"hi"}}`)})
	if out.Err != nil || !strings.Contains(out.Render(), `demo_echo got {"text":"hi"}`) {
		t.Fatalf("tool_invoke = %q %+v", out.Render(), out.Err)
	}
	if err := m.runtime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

// modulesTestDB opens a run store for dispatcher tests.
func modulesTestDB(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	return db
}
