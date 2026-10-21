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
// Pinned stays zero: pinned tool_search schemas are tool results inside
// History, whose LRU is owned by internal/tools (SPEC §5.2, ADR-121).
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

// SessionView is everything one iteration contributes to the request, in
// SPEC §4.5 tier order.
type SessionView struct {
	Phase   Phase
	Stable  StableOptions
	Catalog []CatalogGroup
	// Context is the frozen context tier: [ContextResult.Text], rendered
	// once per session by [Assembler.Context] and reused verbatim (P15).
	Context string
	Tools   []Tool
	// Summaries are the active compaction summary blocks, oldest first,
	// rendered by internal/compaction (ADR-122; tainted ones already
	// wrapped). Build keeps the newest blocks within [CapSummaries].
	Summaries []string
	// Retrieval is the per-turn retrieval tier, best first; constant
	// within a turn. Build wraps each hit as untrusted data within
	// [CapRetrieval].
	Retrieval []Snippet
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
// volatile tier and [ErrUntrusted] for a summary whose <untrusted> wrapper
// does not close. Build does no I/O: every tier is caller data.
func (a *Assembler) Build(_ context.Context, v *SessionView) (Request, Estimate, error) {
	tools, err := FilterTools(v.Phase, v.Tools)
	if err != nil {
		return Request{}, Estimate{}, err
	}
	catalog := a.Catalog(v.Catalog)
	// Rule 3 names tool_search/tool_invoke and "the deferred catalog below",
	// so it is rendered exactly when the catalog is (ADR-067, ADR-121).
	opts := v.Stable
	opts.Deferred = catalog != ""
	stable := Stable(opts)
	// Context() already caps the tier; the clip only guards a caller that
	// passes foreign text, and is a no-op for a rendered tier.
	contextText := clip(v.Context, CapContext, "\n[truncated]", a.tokens)
	system := joinNonEmpty([]string{stable, catalog, contextText})
	summaries, err := a.summaryTier(v.Summaries)
	if err != nil {
		return Request{}, Estimate{}, err
	}
	retrieval := a.retrievalTier(v.Retrieval)
	history, err := v.messagesWithVolatile()
	if err != nil {
		return Request{}, Estimate{}, err
	}
	messages := make([]Message, 0, len(history)+3)
	messages = append(messages, Message{Role: "system", Content: system})
	for _, extra := range []string{summaries, retrieval} {
		if extra != "" {
			messages = append(messages, Message{Role: "user", Content: extra})
		}
	}
	messages = append(messages, history...)
	req := Request{Messages: messages, Tools: tools}
	est, err := a.estimate(v, tools, messages, history)
	if err != nil {
		return Request{}, Estimate{}, err
	}
	est.Stable, est.Catalog, est.Context = a.tokens.Count(stable), a.tokens.Count(catalog), a.tokens.Count(contextText)
	est.Summaries, est.Retrieval = a.tokens.Count(summaries), a.tokens.Count(retrieval)
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

// estimate counts the JSON-shaped parts of the request; Build fills the
// text tiers.
func (a *Assembler) estimate(v *SessionView, tools []Tool, messages, history []Message) (Estimate, error) {
	toolsJSON, err := MarshalTools(tools)
	if err != nil {
		return Estimate{}, err
	}
	historyJSON, err := marshalMessages(history)
	if err != nil {
		return Estimate{}, err
	}
	reqJSON, err := MarshalRequest(Request{Messages: messages, Tools: tools})
	if err != nil {
		return Estimate{}, err
	}
	return Estimate{
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
