package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/exec/classify"
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
