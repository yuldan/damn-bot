package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/loop/event"
)

type runResult struct {
	answer    string
	stop      event.StopReason
	detail    event.StopDetail
	usage     event.Usage
	err       string
	sessionID string
}

// drainRun consumes the turn stream (the caller MUST drain until close, E-29),
// accumulates the final answer and optionally prints stream-JSON lines.
func drainRun(ch <-chan event.Update, output string, stdout io.Writer) runResult {
	var r runResult
	for up := range ch {
		switch up.Kind {
		case event.UpdateTextDelta:
			r.answer += up.Text
		case event.UpdateStop:
			r.stop, r.detail, r.err = up.Stop, up.Detail, up.Err
			if up.Usage != nil {
				r.usage = *up.Usage
			}
		case event.UpdateToolCallStart, event.UpdateToolCallArgs, event.UpdateToolCallEnd,
			event.UpdateToolResult, event.UpdateUsage, event.UpdateNotice:
			// Streamed below; nothing is accumulated here.
		}
		if output == "stream-json" {
			if err := writeStreamEvent(stdout, &up); err != nil && r.err == "" {
				r.err = err.Error()
			}
		}
	}
	return r
}

func writeStreamEvent(w io.Writer, up *event.Update) error {
	line := struct {
		Type   string `json:"type"`
		Text   string `json:"text,omitempty"`
		Tool   string `json:"tool,omitempty"`
		Status string `json:"status,omitempty"`
		Stop   string `json:"stop,omitempty"`
		Detail string `json:"detail,omitempty"`
		Error  string `json:"err,omitempty"`
	}{
		Type: string(up.Kind), Text: up.Text, Tool: up.ToolName, Status: up.Status,
		Stop: string(up.Stop), Detail: string(up.Detail), Error: up.Err,
	}
	return encodeLine(w, line)
}

type turnOutput struct {
	Schema     string   `json:"schema"`
	SessionID  string   `json:"session_id"`
	TurnID     string   `json:"turn_id,omitempty"`
	StopReason string   `json:"stop_reason"`
	StopDetail string   `json:"stop_detail"`
	Answer     string   `json:"answer"`
	Error      string   `json:"error,omitempty"`
	Usage      usageOut `json:"usage"`
}

type usageOut struct {
	Input       int64    `json:"input_tokens"`
	CachedInput int64    `json:"cached_input_tokens"`
	Output      int64    `json:"output_tokens"`
	CostUSD     *float64 `json:"cost_usd,omitempty"`
	Iterations  int      `json:"iterations"`
}

func writeRunResult(r *runResult, output string, stdout io.Writer) int {
	switch output {
	case "text":
		if _, err := fmt.Fprintln(stdout, r.answer); err != nil {
			return 2
		}
	case "json":
		out := turnOutput{
			Schema: "damnbot.turn_result/v1", SessionID: r.sessionID, StopReason: string(r.stop), StopDetail: string(r.detail), Answer: r.answer, Error: r.err,
			Usage: usageOut{Input: r.usage.Input, CachedInput: r.usage.CachedInput, Output: r.usage.Output, CostUSD: r.usage.CostUSD, Iterations: r.usage.Iterations},
		}
		if err := encodeLine(stdout, out); err != nil {
			return 2
		}
	case "stream-json":
		out := struct {
			turnOutput
			Type string `json:"type"`
		}{turnOutput: turnOutput{Schema: "damnbot.turn_result/v1", SessionID: r.sessionID, StopReason: string(r.stop), StopDetail: string(r.detail), Answer: r.answer, Error: r.err}, Type: "result"}
		if err := encodeLine(stdout, out); err != nil {
			return 2
		}
	}
	if r.stop == event.StopEndTurn && r.detail == event.DetailCompleted {
		return 0
	}
	return 2
}

func encodeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write json line: %w", err)
	}
	return nil
}
