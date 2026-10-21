package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// fakeModule is one live module for deferred-tool tests.
type fakeModule struct {
	mu       sync.Mutex
	invoked  int
	lastTool string
	lastArgs json.RawMessage
	lastEnv  host.Env
	res      host.Result
	err      error
}

func (m *fakeModule) Start(context.Context) error { return nil }
func (m *fakeModule) Status() host.Status         { return host.StatusReady }
func (m *fakeModule) Stop(context.Context) error  { return nil }

func (m *fakeModule) Invoke(_ context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invoked++
	m.lastTool, m.lastArgs, m.lastEnv = tool, args, env
	return m.res, m.err
}

func (m *fakeModule) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.invoked
}

// fakeRuntime is the ModuleRuntime double.
type fakeRuntime struct {
	mu        sync.Mutex
	ensureErr error
	ensures   int
	fails     int
	mods      map[string]modules.Module
}

func (r *fakeRuntime) Ensure(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensures++
	return r.ensureErr
}

func (r *fakeRuntime) Module(id string) (modules.Module, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.mods[id]
	return m, ok
}

func (r *fakeRuntime) Fail(context.Context, string, error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fails++
	return nil
}

func (r *fakeRuntime) ensureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensures
}

// deferredFixture wires a dispatcher with the two meta-tools plus an
// optional set of eager tools.
type deferredFixture struct {
	d   *Dispatcher
	j   *journalRec
	sp  *memSpill
	rt  *fakeRuntime
	mod *fakeModule
	cat *Catalog
}

func newDeferredFixture(t *testing.T, defs []DeferredTool, mode Mode, appr Approver, extra ...Tool) *deferredFixture {
	t.Helper()
	mod := &fakeModule{res: host.Result{Content: "module ok"}}
	rt := &fakeRuntime{mods: map[string]modules.Module{"browser": mod}}
	cat, err := NewCatalog(defs)
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	j := &journalRec{}
	sp := newMemSpill()
	reg := mustReg(t, append([]Tool{NewToolSearch(cat, nil), NewToolInvoke()}, extra...)...)
	d, err := NewDispatcher(&Config{
		Registry: reg, Journal: j, Spill: sp, Redactor: testRedactor(), Mode: mode, Approver: appr,
		WorkspaceTrusted: true, Cwd: t.TempDir(), Deferred: &Deferred{Catalog: cat, Runtime: rt},
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return &deferredFixture{d: d, j: j, sp: sp, rt: rt, mod: mod, cat: cat}
}

// deferredDef returns one valid deferred tool in module "browser".
func deferredDef(name, group, effect string, tags ...string) DeferredTool {
	return DeferredTool{
		Name: name, Description: "test tool " + name, Group: group, Module: "browser",
		Effect: effect, Tags: tags,
		Schema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"additionalProperties":false}`),
	}
}

func toolSearchOutcome(t *testing.T, f *deferredFixture, args string) Outcome {
	t.Helper()
	return f.d.Dispatch(context.Background(), Call{Name: ToolSearchName, Args: json.RawMessage(args)})
}

func decodeSearch(t *testing.T, out Outcome) []deferredHit {
	t.Helper()
	if out.Err != nil {
		t.Fatalf("tool_search: %v", out.Err)
	}
	var body struct {
		Tools []deferredHit `json:"tools"`
	}
	if err := json.Unmarshal([]byte(out.Content), &body); err != nil {
		t.Fatalf("decode tool_search: %v", err)
	}
	return body.Tools
}

func TestToolSearchBounded(t *testing.T) {
	t.Run("default_and_max", testSearchDefaultAndMax)
	t.Run("schema_bounds", testSearchSchemaBounds)
	t.Run("group_and_tags", testSearchGroupAndTags)
	t.Run("bounded_catalog_search", testSearchBoundedCatalog)
	t.Run("no_activation", testSearchNoActivation)
}

// searchFixture builds the bounded-search catalog: ten code tools plus one
// web tool.
func searchFixture() []DeferredTool {
	defs := make([]DeferredTool, 0, 11)
	for i := range 10 {
		defs = append(defs, deferredDef(fmt.Sprintf("code_tool_%d", i), "code", "read", "symbol"))
	}
	return append(defs, deferredDef("web_open", "web", "non_idempotent", "navigate"))
}

func testSearchDefaultAndMax(t *testing.T) {
	defs := searchFixture()
	if _, err := NewCatalog(append(defs, defs[0])); err == nil {
		t.Fatal("duplicate deferred tool accepted")
	}
	f := newDeferredFixture(t, defs, ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	if got := len(decodeSearch(t, toolSearchOutcome(t, f, `{"query":"symbol"}`))); got != MaxSearchResults {
		t.Fatalf("default k returned %d, want %d", got, MaxSearchResults)
	}
	if got := len(decodeSearch(t, toolSearchOutcome(t, f, `{"query":"symbol","k":1}`))); got != 1 {
		t.Fatalf("k=1 returned %d", got)
	}
}

func testSearchSchemaBounds(t *testing.T) {
	f := newDeferredFixture(t, searchFixture(), ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	for _, args := range []string{
		`{"query":"symbol","k":9}`,
		`{"query":""}`,
		`{"query":"` + strings.Repeat("x", MaxSearchQuery+1) + `"}`,
	} {
		out := toolSearchOutcome(t, f, args)
		if out.Err == nil || out.Err.Code != CodeInvalidArgs {
			t.Fatalf("%s: code = %v, want invalid_args", args, out.Err)
		}
	}
}

func testSearchGroupAndTags(t *testing.T) {
	f := newDeferredFixture(t, searchFixture(), ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	if got := decodeSearch(t, toolSearchOutcome(t, f, `{"query":"navigate"}`)); len(got) != 1 || got[0].Name != "web_open" {
		t.Fatalf("tag search = %+v", got)
	}
	if got := decodeSearch(t, toolSearchOutcome(t, f, `{"query":"symbol","group":"web"}`)); len(got) != 0 {
		t.Fatalf("group filter = %+v", got)
	}
}

func testSearchBoundedCatalog(t *testing.T) {
	f := newDeferredFixture(t, searchFixture(), ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	if got := f.cat.Search("symbol", "", 99); len(got) != MaxSearchResults {
		t.Fatalf("Search k=99 returned %d", len(got))
	}
	if got := f.cat.Search(strings.Repeat("x", MaxSearchQuery+1), "", 5); got != nil {
		t.Fatalf("over-long query returned %d hits", len(got))
	}
}

func testSearchNoActivation(t *testing.T) {
	f := newDeferredFixture(t, searchFixture(), ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	toolSearchOutcome(t, f, `{"query":"symbol"}`)
	if got := f.rt.ensureCount(); got != 0 {
		t.Fatalf("tool_search activated %d modules", got)
	}
}

func TestToolInvokeRoutesThroughPipeline(t *testing.T) {
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_read", "code", "read")}, ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	f.mod.res = host.Result{Content: "read result"}

	out := f.d.Dispatch(context.Background(), Call{
		TurnID: "t1", CallID: "c1", Name: ToolInvokeName,
		Args: json.RawMessage(`{"name":"code_read","arguments":{"q":"needle"}}`),
	})
	if out.Err != nil {
		t.Fatalf("tool_invoke: %v", out.Err)
	}
	if out.Content != "read result" {
		t.Fatalf("content = %q", out.Content)
	}
	if f.mod.calls() != 1 || f.mod.lastTool != "code_read" {
		t.Fatalf("module calls = %d, tool = %q", f.mod.calls(), f.mod.lastTool)
	}
	if string(f.mod.lastArgs) != `{"q":"needle"}` {
		t.Fatalf("module args = %s", f.mod.lastArgs)
	}
	if f.mod.lastEnv.TurnID != "t1" || f.mod.lastEnv.CallID != "c1" {
		t.Fatalf("module env = %+v", f.mod.lastEnv)
	}
	if f.rt.ensureCount() != 1 {
		t.Fatalf("Ensure calls = %d, want 1", f.rt.ensureCount())
	}
	f.j.mu.Lock()
	dispatches := append([]journalDispatch(nil), f.j.dispatches...)
	results := append([]journalResult(nil), f.j.results...)
	f.j.mu.Unlock()
	if len(dispatches) != 1 || dispatches[0].tool != "code_read" || dispatches[0].effect != EffectRead {
		t.Fatalf("journal dispatch = %+v", dispatches)
	}
	if len(results) != 1 || results[0].tool != "code_read" || !results[0].ok {
		t.Fatalf("journal result = %+v", results)
	}
}

func TestToolInvokeEagerTool(t *testing.T) {
	w := newWorkspace(t)
	w.write("a.txt", "alpha\n")
	f := newDeferredFixture(t, nil, ModeAsk, &approverRec{answer: ApprovalAllowOnce}, w.tools...)
	out := f.d.Dispatch(context.Background(), Call{
		Name: ToolInvokeName, Args: json.RawMessage(`{"name":"read_file","arguments":{"path":"a.txt"}}`),
	})
	if out.Err != nil || !strings.Contains(out.Content, "alpha") {
		t.Fatalf("eager tool_invoke = %+v", out)
	}
	if got := f.rt.ensureCount(); got != 0 {
		t.Fatalf("eager invoke activated %d modules", got)
	}
}

func TestToolInvokeUnknown(t *testing.T) {
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_read", "code", "read")}, ModeAsk, nil)
	out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"nope"}`)})
	if out.Err == nil || out.Err.Code != CodeUnknownTool {
		t.Fatalf("unknown = %+v", out.Err)
	}
	if f.rt.ensureCount() != 0 {
		t.Fatal("unknown tool activated a module")
	}
	if out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"tool_invoke"}`)}); out.Err == nil || out.Err.Code != CodeInvalidArgs {
		t.Fatalf("self invoke = %+v", out.Err)
	}
}

func TestToolInvokeNeedsAttention(t *testing.T) {
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_read", "code", "read")}, ModeAsk, nil)
	f.rt.ensureErr = fmt.Errorf("%w: %q: banned", modules.ErrNeedsAttention, "browser")
	out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"code_read"}`)})
	if out.Err == nil || out.Err.Code != CodeModuleUnavailable {
		t.Fatalf("needs_attention = %+v", out.Err)
	}
	if !strings.Contains(out.Err.Hint, "login") {
		t.Fatalf("hint = %q", out.Err.Hint)
	}
	if f.mod.calls() != 0 {
		t.Fatal("needs_attention module was invoked")
	}
}

// TestToolInvokeApprovalDenied proves the forwarded call, not tool_invoke,
// is what the approver sees, and a denial stops the module.
func TestToolInvokeApprovalDenied(t *testing.T) {
	appr := &approverRec{answer: ApprovalDeny}
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_write", "code", "non_idempotent")}, ModeAsk, appr)
	out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"code_write"}`)})
	if out.Err == nil || out.Err.Code != CodeDenied || !out.Denied {
		t.Fatalf("denial = %+v", out.Err)
	}
	reqs := appr.requests()
	if len(reqs) != 1 || reqs[0].Tool != "code_write" || reqs[0].Effect != EffectNonIdempotent {
		t.Fatalf("approval request = %+v", reqs)
	}
	if f.mod.calls() != 0 {
		t.Fatal("denied tool ran")
	}
	if n, _ := f.j.counts(); n != 0 {
		t.Fatalf("denied call wrote %d dispatch rows", n)
	}
}

// TestToolInvokeApprovalModes keeps headless denial and the taint gate.
func TestToolInvokeApprovalModes(t *testing.T) {
	t.Run("headless", testInvokeHeadlessDenied)
	t.Run("tainted", testInvokeTaintedAsks)
}

func testInvokeHeadlessDenied(t *testing.T) {
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_write", "code", "non_idempotent")}, ModeHeadless, nil)
	out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"code_write"}`)})
	if out.Err == nil || out.Err.Code != CodeDenied {
		t.Fatalf("headless = %+v", out.Err)
	}
	if f.mod.calls() != 0 {
		t.Fatal("headless module ran")
	}
}

func testInvokeTaintedAsks(t *testing.T) {
	appr := &approverRec{answer: ApprovalAllowOnce}
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_write", "code", "non_idempotent")}, ModeAuto, appr)
	f.d.MarkTainted()
	out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"code_write"}`)})
	if out.Err != nil {
		t.Fatalf("tainted invoke = %v", out.Err)
	}
	reqs := appr.requests()
	if len(reqs) != 1 || !reqs[0].Tainted {
		t.Fatalf("tainted approval request = %+v", reqs)
	}
}

func TestToolInvokeTaintAndSpillPreserved(t *testing.T) {
	defs := []DeferredTool{
		deferredDef("browser_read", "web", "read"),
		deferredDef("browser_write", "web", "non_idempotent"),
	}
	appr := &approverRec{answer: ApprovalAllowOnce}
	f := newDeferredFixture(t, defs, ModeAuto, appr)
	f.mod.res = host.Result{Content: "page", UntrustedSource: "browser"}

	out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"browser_read"}`)})
	if out.Err != nil || !out.Tainted || !strings.HasPrefix(out.Content, `<untrusted source="browser">`) {
		t.Fatalf("untrusted module result = %+v", out)
	}
	if !f.d.Tainted() {
		t.Fatal("turn not tainted")
	}
	f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"browser_write"}`)})
	reqs := appr.requests()
	if len(reqs) != 1 || !reqs[0].Tainted || reqs[0].Tool != "browser_write" {
		t.Fatalf("tainted approval = %+v", reqs)
	}

	f.mod.res = host.Result{Content: repeated(InlineChars + 1)}
	out = f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"browser_read"}`)})
	if out.Err != nil {
		t.Fatalf("spill invoke: %v", out.Err)
	}
	if out.Spilled == nil || !strings.Contains(out.Content, "[spill:") {
		t.Fatalf("spill = %+v", out)
	}
	if _, err := f.sp.Get(context.Background(), out.Spilled.SHA); err != nil {
		t.Fatalf("spilled blob missing: %v", err)
	}
}

func TestToolInvokeModuleErrorCode(t *testing.T) {
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_read", "code", "read")}, ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	f.mod.err = &host.Error{Code: "not_found", Message: "missing", Hint: "retry"}
	out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"code_read"}`)})
	if out.Err == nil || out.Err.Code != CodeNotFound || out.Err.Hint != "retry" {
		t.Fatalf("module error = %+v", out.Err)
	}
	f.mod.err = &host.Error{Code: "made_up", Message: "x"}
	out = f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"code_read"}`)})
	if out.Err == nil || out.Err.Code != CodeInternal {
		t.Fatalf("non-closed module code = %+v", out.Err)
	}
	f.mod.err = errors.New("boom")
	out = f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"code_read"}`)})
	if out.Err == nil || out.Err.Code != CodeInternal {
		t.Fatalf("plain module error = %+v", out.Err)
	}
}

func TestToolInvokeRequiresDeferredConfig(t *testing.T) {
	// tool_invoke registered without a Deferred config fails closed.
	d, _, _ := newWorkspace(t).dispatcher(t, ModeAsk, nil, NewToolInvoke())
	out := d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"read_file"}`)})
	if out.Err == nil || out.Err.Code != CodeModuleUnavailable {
		t.Fatalf("missing deferred config = %+v", out.Err)
	}
}

func TestCatalogValidation(t *testing.T) {
	bad := []DeferredTool{
		{Name: "x", Description: "d", Schema: json.RawMessage(`{"type":"object","pattern":"["}`)},
		{Name: "x", Description: "d", Schema: json.RawMessage(`{`)},
		{Name: "", Description: "d", Schema: json.RawMessage(`{}`)},
		{Name: "x", Description: " ", Schema: json.RawMessage(`{}`)},
	}
	for i, dt := range bad {
		if _, err := NewCatalog([]DeferredTool{dt}); err == nil {
			t.Errorf("case %d: invalid catalog entry accepted", i)
		}
	}
	cat, err := NewCatalog([]DeferredTool{deferredDef("code_b", "code", "read"), deferredDef("code_a", "code", "read")})
	if err != nil {
		t.Fatal(err)
	}
	if names := cat.Names(); len(names) != 2 || names[0] != "code_a" || names[1] != "code_b" {
		t.Fatalf("Names = %v", names)
	}
	if _, ok := cat.Lookup("code_a"); !ok {
		t.Fatal("Lookup failed")
	}
}

func TestDeferredModulePanic(t *testing.T) {
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_read", "code", "read")}, ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	// A nil function call panics inside Invoke; boundary recovers it.
	f.rt.mods["browser"] = &panicInvokeModule{inner: f.mod}
	out := f.d.Dispatch(context.Background(), Call{Name: ToolInvokeName, Args: json.RawMessage(`{"name":"code_read"}`)})
	if out.Err == nil || out.Err.Code != CodeInternal {
		t.Fatalf("panic = %+v", out.Err)
	}
	f.rt.mu.Lock()
	fails := f.rt.fails
	f.rt.mu.Unlock()
	if fails != 1 {
		t.Fatalf("module not marked failed after panic (%d)", fails)
	}
}

type panicInvokeModule struct{ inner *fakeModule }

func (m *panicInvokeModule) Start(context.Context) error { return nil }
func (m *panicInvokeModule) Status() host.Status         { return host.StatusReady }
func (m *panicInvokeModule) Stop(context.Context) error  { return nil }

func (m *panicInvokeModule) Invoke(context.Context, string, json.RawMessage, host.Env) (host.Result, error) {
	empty := []int{}
	i := len(empty)
	return host.Result{Content: fmt.Sprint(empty[i])}, nil
}
