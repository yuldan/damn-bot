package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/loop"
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
			assistantAt[r.ID] = len(out)
			out = append(out, loop.Message{Role: "assistant", Content: r.Content, ToolCalls: assistantToolCalls(r.Payload)})
		case "tool_result":
			// The dispatcher journal and the loop both record a tool_result;
			// keep one message per tool_call_id, preferring the non-empty
			// loop result.
			if r.ToolCallID == "" {
				continue
			}
			if i := findToolMessage(out, r.ToolCallID); i >= 0 {
				if out[i].Content == "" && r.Content != "" {
					out[i].Content = r.Content
				}
				continue
			}
			out = append(out, loop.Message{Role: "tool", Content: r.Content, ToolCallID: r.ToolCallID})
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

// effectsNotice renders the turnless notice a failed turn left for executed
// side effects (SEC11-3), or "" for any other system_notice.
func effectsNotice(payload string) string {
	var p struct {
		Kind    string   `json:"kind"`
		TurnID  string   `json:"turn_id"`
		Effects []string `json:"effects"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil || p.Kind != "failed_turn_effects" || len(p.Effects) == 0 {
		return ""
	}
	return "A previous failed turn (" + p.TurnID + ") already executed: " + strings.Join(p.Effects, ", ") +
		". Do not repeat these side effects; use recall to inspect the transcript if needed."
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
