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

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/tools"
)

// ctxJournal writes tool_dispatch/tool_result events to the session transcript.
// The dispatcher is per session, so the journal is bound to one session id.
type ctxJournal struct {
	db  *ctxdb.Store
	sid string
}

func (j *ctxJournal) ToolDispatch(ctx context.Context, turnID, callID, tool string, effect tools.Effect, hash [32]byte) error {
	payload := fmt.Sprintf(`{"effect":%q,"args_sha":"%x"}`, effect, hash)
	_, err := j.db.Append(ctx, j.sid, nil, ctxdb.Event{
		Kind: "tool_dispatch", TurnID: turnID, ToolCallID: callID, ToolName: tool, Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("journal tool_dispatch: %w", err)
	}
	return nil
}

func (j *ctxJournal) ToolResult(ctx context.Context, turnID, callID, tool string, ok bool, code, content string) error {
	payload, err := json.Marshal(struct {
		OK      bool   `json:"ok"`
		Code    string `json:"code,omitempty"`
		Content string `json:"content,omitempty"`
	}{OK: ok, Code: code, Content: content})
	if err != nil {
		return fmt.Errorf("journal tool_result payload: %w", err)
	}
	_, err = j.db.Append(ctx, j.sid, nil, ctxdb.Event{
		Kind: "tool_result", TurnID: turnID, ToolCallID: callID, ToolName: tool, Payload: string(payload),
	})
	if err != nil {
		return fmt.Errorf("journal tool_result: %w", err)
	}
	return nil
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
	sid      string
	cwd      string
	fail     func(error) error
	// inlineCap is the route window budget in tokens the tool inline aggregate
	// scales with (SPEC P5 amendment, GAP-COST-08). It is a wiring value, not a
	// model-window observation, so it stays out of runModel.window.
	inlineCap int
}

// newRunWiring opens the run store, the sandbox and codetools engine, the exec
// runner and its persistent sessions, then wires the shell, file, and skill
// tools into a registry. Failures after the sessions exist go through the
// returned fail closure so nothing leaks.
func newRunWiring(ctx context.Context, cfg *config.Config, dataDir, modelRef string, o *runOptions, red *redact.Redactor, spill tools.Spill, configDir, home string) (*runWiring, error) {
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "run"})
	if err != nil {
		return nil, fmt.Errorf("open ctxdb: %w", err)
	}
	sid, cwd, err := newRunSession(ctx, db, modelRef)
	if err != nil {
		return nil, closeDB(ctx, db, err)
	}
	roots := append([]string{cwd}, cfg.Approval.AddDirs...)
	sb, err := sandbox.New(&sandbox.Config{Roots: roots, DenyPaths: cfg.DenyPaths(configDir), Home: home})
	if err != nil {
		return nil, closeDB(ctx, db, fmt.Errorf("sandbox: %w", err))
	}
	engine, err := codetools.New(&codetools.Config{Sandbox: sb, Roots: roots, Home: home})
	if err != nil {
		return nil, closeDB(ctx, db, fmt.Errorf("codetools: %w", err))
	}
	runner := exec.New(ctx)
	envPolicy := &exec.EnvPolicy{Allow: cfg.Exec.EnvAllow, Set: cfg.Exec.Env, PassSSHAgent: cfg.Exec.PassSSHAgent}
	// Persistent sessions share the runner but never receive secret refs
	// (ADR-088): direct is empty and Env only inherits the allowlist.
	sessions := runner.NewSessions(filepath.Join(dataDir, "exec", "sess"), envPolicy.Env(os.Environ(), true, "", "", ""))
	fail := func(cause error) error {
		engine.Close()
		if cerr := sessions.Close(); cerr != nil {
			cause = errors.Join(cause, fmt.Errorf("close sessions: %w", cerr))
		}
		if cerr := runner.CloseContext(ctx); cerr != nil {
			cause = errors.Join(cause, fmt.Errorf("close runner: %w", cerr))
		}
		return closeDB(ctx, db, cause)
	}
	allow, monetary, err := shellGate(cfg, configDir)
	if err != nil {
		return nil, fail(err)
	}
	shellTool, err := tools.NewShell(&tools.ShellConfig{
		Runner: runner, Sessions: sessions, Spill: spill, Redactor: red, Allow: allow, Roots: roots, Home: home,
		Cwd: cwd, DenyPaths: cfg.DenyPaths(configDir), ParentEnv: os.Environ(),
		EnvPolicy:        envPolicy,
		WorkspaceTrusted: true, UnsafeAutoShell: o.unsafeShell, Dialect: runtimeDialect(),
		MonetaryPaths: monetary, Canonicalize: monetaryKey,
	})
	if err != nil {
		return nil, fail(fmt.Errorf("shell tool: %w", err))
	}
	skillsMgr, err := newSkillManager(ctx, db, dataDir, configDir, red)
	if err != nil {
		return nil, fail(err)
	}
	list := tools.FileTools(engine, sb)
	list = append(list, shellTool, tools.NewTodo(), tools.NewClarify(nil),
		tools.NewSkillsList(skillsMgr), tools.NewSkillView(skillsMgr), tools.NewSkillManage(skillsMgr))
	reg, err := tools.NewRegistry(list...)
	if err != nil {
		return nil, fail(fmt.Errorf("registry: %w", err))
	}
	return &runWiring{
		db:        db,
		engine:    engine,
		runner:    runner,
		sessions:  sessions,
		registry:  reg,
		sid:       sid,
		cwd:       cwd,
		fail:      fail,
		inlineCap: cfg.Loop.TurnMaxInputTok,
	}, nil
}
