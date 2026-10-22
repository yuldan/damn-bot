package compaction

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// Event is one transcript row as compaction reads it: an events row plus the
// image attachments of a user message. A Store fills it in seq order;
// compaction never writes events through this type.
type Event struct {
	Seq        int64
	Kind       event.Kind
	TurnID     string
	Role       string
	Content    string
	ToolCallID string
	ToolName   string
	Payload    string   // JSON object (events.payload)
	BlobSHA    string   // events.blob_sha (spilled tool output)
	Images     []string // sha256 of image attachments, in order
}

// ToolCall is one assistant tool call of the request view.
type ToolCall struct {
	ID, Name, Arguments string
}

// Message is one model-visible message of the request view. Images lists the
// attachments still sent in full; the assembler resolves them.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	Images     []string
}

// item is a Message plus what the prune rules and the summary cut need.
type item struct {
	Message
	seq     int64  // first event that produced the message
	tool    string // tool name of a tool result
	args    string // raw arguments of the matching call
	path    string // "path" argument of the matching call, verbatim
	blob    string // spill blob of a tool result
	failed  bool   // the tool reported an error
	tainted bool   // untrusted content (SPEC §14.5, ADR-070)
	stubbed bool   // a stage-1 rule already replaced the content
}

// history turns events into model-visible messages. A tool result is paired
// with the latest assistant call carrying its id; the dispatcher journal and
// the loop both record a tool_result per call, so later rows of the same
// call merge into the first. A result without a call in events is dropped
// and counted: sending it alone would make the request invalid (EC-CTX-011).
func history(events []Event) (items []item, orphans int) {
	items = make([]item, 0, len(events))
	calls := map[string]ToolCall{}
	results := map[string]int{}
	for i := range events {
		e := &events[i]
		switch e.Kind { //nolint:exhaustive // only chat kinds become messages; llm_call, turn_start, summary_ref, … are bookkeeping
		case event.UserMessage:
			items = append(items, item{Message: Message{Role: "user", Content: e.Content, Images: slices.Clone(e.Images)}, seq: e.Seq})
		case event.AssistantMessage:
			m := Message{Role: "assistant", Content: e.Content, ToolCalls: decodeCalls(e.Payload)}
			for _, c := range m.ToolCalls {
				calls[c.ID] = c
				delete(results, c.ID) // a reused id starts a new pair
			}
			items = append(items, item{Message: m, seq: e.Seq})
		case event.ToolResult:
			c, ok := calls[e.ToolCallID]
			if !ok {
				orphans++
				continue
			}
			items = addResult(items, e, &c, results)
		}
	}
	return items, orphans
}

func addResult(items []item, e *Event, c *ToolCall, results map[string]int) []item {
	failed, tainted := resultFlags(e)
	if j, dup := results[e.ToolCallID]; dup {
		r := &items[j]
		if r.Content == "" {
			r.Content = e.Content
		}
		r.blob = cmp.Or(r.blob, e.BlobSHA)
		r.failed = r.failed || failed
		r.tainted = r.tainted || tainted
		return items
	}
	results[e.ToolCallID] = len(items)
	return append(items, item{
		Message: Message{Role: "tool", Content: e.Content, ToolCallID: e.ToolCallID},
		seq:     e.Seq, tool: cmp.Or(e.ToolName, c.Name), args: c.Arguments, path: argPath(c.Arguments),
		blob: e.BlobSHA, failed: failed, tainted: tainted,
	})
}

// resultFlags reads the loop payload (error, tainted) and the dispatcher
// journal payload (ok). An unreadable payload is neither failed nor tainted
// by itself; the content check still marks wrapped output.
func resultFlags(e *Event) (failed, tainted bool) {
	var p struct {
		OK      *bool           `json:"ok"`
		Error   json.RawMessage `json:"error"`
		Tainted bool            `json:"tainted"`
	}
	if json.Unmarshal([]byte(e.Payload), &p) == nil {
		failed = (p.OK != nil && !*p.OK) || (len(p.Error) > 0 && string(p.Error) != "null")
		tainted = p.Tainted
	}
	return failed, tainted || strings.Contains(e.Content, "<untrusted")
}

// decodeCalls reads assistant tool calls; a malformed payload yields none.
func decodeCalls(payload string) []ToolCall {
	var p struct {
		ToolCalls []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil || len(p.ToolCalls) == 0 {
		return nil
	}
	out := make([]ToolCall, len(p.ToolCalls))
	for i := range p.ToolCalls {
		out[i] = ToolCall(p.ToolCalls[i])
	}
	return out
}

// argPath is the verbatim "path" argument. Paths are compared byte for byte:
// two spellings of one file are never merged, which only keeps more content.
func argPath(args string) string {
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return ""
	}
	return a.Path
}

func messages(items []item) []Message {
	out := make([]Message, len(items))
	for i := range items {
		out[i] = items[i].Message
	}
	return out
}
