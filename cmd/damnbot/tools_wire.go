package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/tools"
)

// wireModules builds the deferred catalog of the frozen module registry and
// appends its meta tools to list (ADR-119), so newRunWiring stays short.
func wireModules(list []tools.Tool) (runModules, *tools.Registry, error) {
	mods, err := newRunModules(modules.Default(), nil)
	if err != nil {
		return runModules{}, nil, err
	}
	reg, err := tools.NewRegistry(append(list, mods.meta...)...)
	if err != nil {
		return runModules{}, nil, fmt.Errorf("registry: %w", err)
	}
	return mods, reg, nil
}

// stopModules shuts the module runtime down and joins the cause, so the fail
// path of newRunWiring and runModel.close release modules the same way.
func stopModules(ctx context.Context, mods *runModules, cause error) error {
	if mods.runtime == nil {
		return cause
	}
	if serr := mods.runtime.Shutdown(ctx); serr != nil {
		return errors.Join(cause, fmt.Errorf("stop modules: %w", serr))
	}
	return cause
}

// closeWiring releases everything newRunWiring opened and joins every error
// with the cause, so the fail path and runModel.close report identically.
func closeWiring(ctx context.Context, cause error, mods *runModules, engine *codetools.Engine, sessions *exec.Sessions, runner *exec.Runner, db *ctxdb.Store, sid string) error {
	cause = stopModules(ctx, mods, cause)
	engine.Close()
	if cerr := sessions.Close(); cerr != nil {
		cause = errors.Join(cause, fmt.Errorf("close sessions: %w", cerr))
	}
	if cerr := runner.CloseContext(ctx); cerr != nil {
		cause = errors.Join(cause, fmt.Errorf("close runner: %w", cerr))
	}
	return closeDB(ctx, db, sid, cause)
}

func runtimeDialect() classify.Dialect {
	switch runtime.GOOS {
	case "windows":
		return classify.Cmd
	default:
		return classify.Bash
	}
}

// History reconstructs the chat history from the session transcript, including
// assistant tool calls and tool results, so every loop iteration sees them.
// The store read is fork-aware and bounded (SPEC §4.6); a resumed fork keeps
// its ancestor prefix.
func (s *ctxStore) History(ctx context.Context, sid string) ([]loop.Message, error) {
	rows, err := s.db.History(ctx, sid, 0, ctxdb.WithHistoryLimit(ctxdb.DefaultHistoryLimit))
	if err != nil {
		return nil, fmt.Errorf("history query: %w", err)
	}
	out := make([]loop.Message, 0, len(rows))
	assistantAt := make(map[int64]int, len(rows))
	for i := range rows {
		r := &rows[i]
		switch r.Kind {
		case "user_message":
			out = append(out, loop.Message{Role: "user", Content: r.Content})
		case "assistant_message":
			out = appendAssistant(out, assistantAt, r)
		case "tool_result":
			out = appendToolResult(out, r)
		case "system_notice":
			if c := effectsNotice(r.Payload); c != "" {
				out = append(out, loop.Message{Role: "system", Content: c})
			}
		}
	}
	if err := s.loadCarriers(ctx, sid, assistantAt, out); err != nil {
		return nil, fmt.Errorf("history carriers: %w", err)
	}
	return out, nil
}

// appendAssistant appends a replayable assistant message and records its
// carrier anchor; a partial answer without tool calls is skipped
// (EC-PROV-008).
func appendAssistant(out []loop.Message, assistantAt map[int64]int, r *ctxdb.Event) []loop.Message {
	if skipAssistant(r) {
		return out
	}
	assistantAt[r.ID] = len(out)
	return append(out, loop.Message{Role: "assistant", Content: r.Content, ToolCalls: assistantToolCalls(r.Payload)})
}

// appendToolResult keeps one tool message per tool_call_id: the dispatcher
// journal and the loop both record a result, and the non-empty loop result
// wins.
func appendToolResult(out []loop.Message, r *ctxdb.Event) []loop.Message {
	if r.ToolCallID == "" {
		return out
	}
	if i := findToolMessage(out, r.ToolCallID); i >= 0 {
		if out[i].Content == "" && r.Content != "" {
			out[i].Content = r.Content
		}
		return out
	}
	return append(out, loop.Message{Role: "tool", Content: r.Content, ToolCallID: r.ToolCallID})
}

// skipAssistant reports an assistant event that must not replay: a partial
// (finish_reason=length) answer without tool calls. A partial event that
// carries calls keeps its tool_result pairing.
func skipAssistant(r *ctxdb.Event) bool {
	return partialOutput(r.Payload) && len(assistantToolCalls(r.Payload)) == 0
}

// partialOutput reports an assistant event the loop marked partial
// (finish_reason=length, EC-PROV-008). It stays in the transcript for the
// user but never replays as a final assistant message.
func partialOutput(payload string) bool {
	if payload == "" {
		return false
	}
	var p struct {
		Partial bool `json:"partial"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return false
	}
	return p.Partial
}

// effectsNotice renders the turnless notice a failed turn left for executed
// side effects (SEC11-3), or "" for any other system_notice.
func effectsNotice(payload string) string {
	var p struct {
		Kind      string   `json:"kind"`
		TurnID    string   `json:"turn_id"`
		Effects   []string `json:"effects"`
		Uncertain []string `json:"uncertain"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil || p.Kind != "failed_turn_effects" ||
		len(p.Effects) == 0 && len(p.Uncertain) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("A previous failed turn (" + p.TurnID + ") ")
	if len(p.Effects) > 0 {
		b.WriteString("already executed: " + strings.Join(p.Effects, ", ") + ". ")
	}
	if len(p.Uncertain) > 0 {
		b.WriteString("may have run with unknown outcome: " + strings.Join(p.Uncertain, ", ") + ". ")
	}
	b.WriteString("Do not repeat these side effects; use recall to inspect the transcript if needed.")
	return b.String()
}

func findToolMessage(msgs []loop.Message, callID string) int {
	for i := range msgs {
		if msgs[i].Role == "tool" && msgs[i].ToolCallID == callID {
			return i
		}
	}
	return -1
}

// assistantToolCalls decodes the tool_calls stored in an assistant payload. A
// malformed payload yields no calls: the assistant text is still valid history.
func assistantToolCalls(payload string) []loop.ToolCall {
	var p struct {
		ToolCalls []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil
	}
	calls := make([]loop.ToolCall, 0, len(p.ToolCalls))
	for _, c := range p.ToolCalls {
		calls = append(calls, loop.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
	}
	return calls
}

// runWiring is every resource startRunModel opens before the model call: the
// run store, the sandboxed tool stack, and the registered tool set, plus the
// cleanup closure that releases them when a later step fails.
type runWiring struct {
	db       *ctxdb.Store
	engine   *codetools.Engine
	runner   *exec.Runner
	sessions *exec.Sessions
	registry *tools.Registry
	modules  runModules
	sid      string
	cwd      string
	// modelRef is the effective provider/model after resume resolution: a
	// resumed session may supply the model when no flag or config did.
	modelRef string
	fail     func(error) error
	// inlineCap is the route window budget in tokens the tool inline aggregate
	// scales with (SPEC P5 amendment, GAP-COST-08). It is a wiring value, not a
	// model-window observation, so it stays out of runModel.window.
	inlineCap int
	// spill routes tool output above InlineChars into the ctxdb blob store
	// (SEC7-66/68); the same instance serves the shell tool and the turn.
	spill tools.Spill
	// trusted is the workspace trust decision for this run (SPEC §14): the
	// session cwd is inside a project registered in the trust store. It gates
	// shell_allow/--unsafe-auto-shell and the <untrusted source="workspace">
	// wrapper of the context file tier.
	trusted bool
	// reader reads the workspace context files (AGENTS.md and friends) through
	// the sandbox.
	reader assemble.FileReader
}

// openWiredSession opens the run store and resolves the session (new or
// resumed), returning the effective model. Extracted from newRunWiring so its
// cyclomatic complexity stays inside the E-51 gate.
func openWiredSession(ctx context.Context, dataDir, modelRef string, o *runOptions, codec ctxdb.Codec) (*ctxdb.Store, string, string, string, error) {
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "run", Codec: codec})
	if err != nil {
		return nil, "", "", "", fmt.Errorf("open ctxdb: %w", err)
	}
	sid, cwd, resumedModel, err := runSession(ctx, db, modelRef, o)
	if err != nil {
		return nil, "", "", "", closeDB(ctx, db, "", err)
	}
	if modelRef == "" {
		modelRef = resumedModel
	}
	if modelRef == "" {
		return nil, "", "", "", closeDB(ctx, db, sid, errors.New("--model or provider.model is required to resume a session without one"))
	}
	return db, sid, cwd, modelRef, nil
}

// newRunWiring opens the run store, the sandbox and codetools engine, the exec
// runner and its persistent sessions, then wires the shell, file, and skill
// tools into a registry. Failures after the sessions exist go through the
// returned fail closure so nothing leaks.
func newRunWiring(ctx context.Context, cfg *config.Config, dataDir, modelRef string, o *runOptions, red *redact.Redactor, configDir, home string, codec ctxdb.Codec) (*runWiring, error) {
	db, sid, cwd, modelRef, err := openWiredSession(ctx, dataDir, modelRef, o, codec)
	if err != nil {
		return nil, err
	}
	spill := ctxSpill{db: db}
	roots := append([]string{cwd}, cfg.Approval.AddDirs...)
	sb, err := sandbox.New(&sandbox.Config{Roots: roots, DenyPaths: cfg.DenyPaths(configDir), Home: home})
	if err != nil {
		return nil, closeDB(ctx, db, sid, fmt.Errorf("sandbox: %w", err))
	}
	// The workspace trust decision (SPEC §14): headless runs are untrusted
	// unless the canonical cwd is registered in the trust store, so an empty
	// or unreadable store fails closed.
	trusted, err := workspaceTrusted(cfg, configDir, cwd, home)
	if err != nil {
		return nil, closeDB(ctx, db, sid, fmt.Errorf("workspace trust: %w", err))
	}
	scope, err := journalScope(roots, cfg.DenyPaths(configDir), home)
	if err != nil {
		return nil, closeDB(ctx, db, sid, err)
	}
	engine, err := codetools.New(&codetools.Config{Sandbox: sb, Roots: roots, Home: home})
	if err != nil {
		return nil, closeDB(ctx, db, sid, fmt.Errorf("codetools: %w", err))
	}
	runner := exec.New(ctx)
	envPolicy := &exec.EnvPolicy{Allow: cfg.Exec.EnvAllow, Set: cfg.Exec.Env, PassSSHAgent: cfg.Exec.PassSSHAgent}
	// Persistent sessions share the runner but never receive secret refs (ADR-088).
	sessions := runner.NewSessions(filepath.Join(dataDir, "exec", "sess"), envPolicy.Env(os.Environ(), true, "", "", ""))
	var mods runModules
	fail := func(cause error) error {
		return closeWiring(ctx, cause, &mods, engine, sessions, runner, db, sid)
	}
	allow, monetary, err := shellGate(ctx, cfg, configDir, cwd)
	if err != nil {
		return nil, fail(err)
	}
	// Skills discovered in a new location (project discovery,
	// skills.external_dirs) go through quarantine and stay invisible until the
	// user approves them (ADR-175).
	if _, qerr := quarantineImportedSkills(ctx, db, red, cfg, configDir, cwd); qerr != nil {
		return nil, fail(qerr)
	}
	shellTool, err := tools.NewShell(&tools.ShellConfig{
		Runner: runner, Sessions: sessions, Spill: spill, Redactor: red, Allow: allow, Roots: roots, Home: home,
		Cwd: cwd, DenyPaths: cfg.DenyPaths(configDir), ParentEnv: os.Environ(),
		EnvPolicy:        envPolicy,
		WorkspaceTrusted: trusted, UnsafeAutoShell: o.unsafeShell, Dialect: runtimeDialect(),
		MonetaryPaths: monetary, Canonicalize: monetaryKey,
	})
	if err != nil {
		return nil, fail(fmt.Errorf("shell tool: %w", err))
	}
	skillsMgr, err := newSkillManager(ctx, db, dataDir, configDir, red)
	if err != nil {
		return nil, fail(err)
	}
	list, err := journaledFileTools(db, sid, scope, engine, sb)
	if err != nil {
		return nil, fail(err)
	}
	list = append(list, shellTool, tools.NewTodo(), tools.NewClarify(nil),
		tools.NewSkillsList(skillsMgr), tools.NewSkillView(skillsMgr), tools.NewSkillManage(skillsMgr))
	var reg *tools.Registry
	mods, reg, err = wireModules(list)
	if err != nil {
		return nil, fail(err)
	}
	return &runWiring{
		db:        db,
		engine:    engine,
		runner:    runner,
		sessions:  sessions,
		registry:  reg,
		modules:   mods,
		sid:       sid,
		cwd:       cwd,
		modelRef:  modelRef,
		fail:      fail,
		inlineCap: cfg.Loop.TurnMaxInputTok,
		spill:     spill,
		trusted:   trusted,
		reader:    sandboxReader{sb: sb, root: cwd},
	}, nil
}

func (s *ctxStore) viewSide(ctx context.Context, sid string) (notices []loop.Message, eventID map[int64]int64, err error) {
	rows, err := s.db.History(ctx, sid, 0, ctxdb.WithHistoryLimit(ctxdb.DefaultHistoryLimit))
	if err != nil {
		return nil, nil, fmt.Errorf("history query: %w", err)
	}
	eventID = make(map[int64]int64, len(rows))
	for i := range rows {
		r := &rows[i]
		switch r.Kind {
		case "assistant_message":
			eventID[r.Seq] = r.ID
		case "system_notice":
			if c := effectsNotice(r.Payload); c != "" {
				notices = append(notices, loop.Message{Role: "system", Content: c})
			}
		}
	}
	return notices, eventID, nil
}
