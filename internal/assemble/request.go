package assemble

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Message is one chat message in the assembled request.
type Message struct {
	Role    string
	Content string
}

// Request is the openai-chat wire request assembled for one iteration.
type Request struct {
	Messages []Message
	Tools    []Tool
}

// Estimate is the per-tier token accounting of one request (SPEC §4.5).
// Pinned, Summaries and Retrieval stay zero in F1: schema pinning, compaction
// summaries and memory retrieval arrive in F2.
type Estimate struct {
	Stable    int
	Catalog   int
	Context   int
	Volatile  int
	Tools     int
	Pinned    int
	Summaries int
	Retrieval int
	History   int
	Total     int
}

// SessionView is everything one iteration contributes to the request.
type SessionView struct {
	Phase   Phase
	Stable  StableOptions
	Catalog []CatalogGroup
	Context *ContextInput
	Tools   []Tool
	// Turn is the frozen volatile tier, shared by every iteration of the
	// turn. Nil is allowed only for callers without a turn (tests, F2
	// tooling).
	Turn *Turn
	// History is the append-only message list. The volatile tier is appended
	// to the last user message; the message sequence itself is never
	// rewritten (ADR-067 butir 2).
	History []Message
}

// Build renders one request and its per-tier estimate. It returns
// [ErrNoUserMessage] when a frozen turn has no user message to carry the
// volatile tier, and [ErrDeps] when context files need a reader.
func (a *Assembler) Build(ctx context.Context, v *SessionView) (Request, Estimate, error) {
	tools, err := FilterTools(v.Phase, v.Tools)
	if err != nil {
		return Request{}, Estimate{}, err
	}
	catalog := Catalog(v.Catalog, a.tokens)
	ctxResult, err := a.Context(ctx, v.Context)
	if err != nil {
		return Request{}, Estimate{}, err
	}
	// Rule 3 names tool_search/tool_invoke and "the deferred catalog below",
	// so it is rendered exactly when the catalog is (ADR-067, ADR-121).
	opts := v.Stable
	opts.Deferred = catalog != ""
	stable := Stable(opts)
	system := joinNonEmpty([]string{stable, catalog, ctxResult.Text})
	history, err := v.messagesWithVolatile()
	if err != nil {
		return Request{}, Estimate{}, err
	}
	messages := make([]Message, 0, len(history)+1)
	messages = append(messages, Message{Role: "system", Content: system})
	messages = append(messages, history...)
	req := Request{Messages: messages, Tools: tools}
	est, err := a.estimate(v, stable, catalog, ctxResult.Text, tools, messages)
	if err != nil {
		return Request{}, Estimate{}, err
	}
	return req, est, nil
}

func (v *SessionView) messagesWithVolatile() ([]Message, error) {
	messages := append([]Message(nil), v.History...)
	if v.Turn == nil {
		return messages, nil
	}
	volatile := v.Turn.String()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		if volatile != "" {
			if messages[i].Content != "" {
				messages[i].Content += "\n\n"
			}
			messages[i].Content += volatile
		}
		return messages, nil
	}
	return nil, fmt.Errorf("assemble: turn: %w", ErrNoUserMessage)
}

func (a *Assembler) estimate(
	v *SessionView, stable, catalog, contextText string, tools []Tool, messages []Message,
) (Estimate, error) {
	toolsJSON, err := MarshalTools(tools)
	if err != nil {
		return Estimate{}, err
	}
	historyJSON, err := marshalMessages(messages[1:])
	if err != nil {
		return Estimate{}, err
	}
	reqJSON, err := MarshalRequest(Request{Messages: messages, Tools: tools})
	if err != nil {
		return Estimate{}, err
	}
	return Estimate{
		Stable:   a.tokens.Count(stable),
		Catalog:  a.tokens.Count(catalog),
		Context:  a.tokens.Count(contextText),
		Volatile: a.tokens.Count(v.Turn.String()),
		Tools:    a.tokens.Count(string(toolsJSON)),
		History:  a.tokens.Count(string(historyJSON)),
		Total:    a.tokens.Count(string(reqJSON)),
	}, nil
}

func joinNonEmpty(parts []string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), "\n\n")
}

// MarshalRequest renders the openai-chat wire object: messages + tools, no
// HTML escaping, deterministic field order.
func MarshalRequest(req Request) ([]byte, error) {
	tools, err := MarshalTools(req.Tools)
	if err != nil {
		return nil, err
	}
	wire := wireRequest{
		Messages: make([]wireMessage, len(req.Messages)),
		Tools:    tools,
	}
	for i, m := range req.Messages {
		wire.Messages[i] = wireMessage(m)
	}
	return marshalNoEscape(wire)
}

func marshalMessages(messages []Message) ([]byte, error) {
	wire := make([]wireMessage, len(messages))
	for i, m := range messages {
		wire[i] = wireMessage(m)
	}
	return marshalNoEscape(wire)
}

type wireRequest struct {
	Messages []wireMessage   `json:"messages"`
	Tools    json.RawMessage `json:"tools"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
