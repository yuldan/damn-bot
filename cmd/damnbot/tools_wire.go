package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

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
	return err
}

func (j *ctxJournal) ToolResult(ctx context.Context, turnID, callID, tool string, ok bool, code, content string) error {
	payload, err := json.Marshal(struct {
		OK      bool   `json:"ok"`
		Code    string `json:"code,omitempty"`
		Content string `json:"content,omitempty"`
	}{OK: ok, Code: code, Content: content})
	if err != nil {
		return err
	}
	_, err = j.db.Append(ctx, j.sid, nil, ctxdb.Event{
		Kind: "tool_result", TurnID: turnID, ToolCallID: callID, ToolName: tool, Payload: string(payload),
	})
	return err
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
func (s *ctxStore) History(ctx context.Context, sid string) ([]loop.Message, error) {
	const q = `SELECT coalesce(json_group_array(json_object(
		'kind',kind,'role',coalesce(role,''),'content',coalesce(content,''),
		'tool_call_id',coalesce(tool_call_id,''),'payload',coalesce(payload,'{}'))),'[]')
		FROM (SELECT * FROM events WHERE session_id = ? ORDER BY seq)`
	var raw string
	if err := s.db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.Get(q, []any{sid}, &raw) }); err != nil {
		return nil, err
	}
	var rows []struct {
		Kind       string `json:"kind"`
		Role       string `json:"role"`
		Content    string `json:"content"`
		ToolCallID string `json:"tool_call_id"`
		Payload    string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, err
	}
	out := make([]loop.Message, 0, len(rows))
	for _, r := range rows {
		switch r.Kind {
		case "user_message":
			out = append(out, loop.Message{Role: "user", Content: r.Content})
		case "assistant_message":
			m := loop.Message{Role: "assistant", Content: r.Content}
			var p struct {
				ToolCalls []struct {
					ID        string `json:"id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"tool_calls"`
			}
			_ = json.Unmarshal([]byte(r.Payload), &p)
			for _, c := range p.ToolCalls {
				m.ToolCalls = append(m.ToolCalls, loop.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.Arguments})
			}
			out = append(out, m)
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
		}
	}
	return out, nil
}

func findToolMessage(msgs []loop.Message, callID string) int {
	for i := range msgs {
		if msgs[i].Role == "tool" && msgs[i].ToolCallID == callID {
			return i
		}
	}
	return -1
}
