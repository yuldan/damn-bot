package provider

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// callAsm accumulates one streamed tool call.
type callAsm struct {
	idx    int
	hasIdx bool
	id     string
	name   []byte // appended, not concatenated (PROV-M1)
	args   []byte
}

// assembler turns stream deltas into a Response (owned copies).
type assembler struct {
	content     []byte
	reasonText  []byte
	reasonParts []json.RawMessage // set once a non-string reasoning arrives
	reasonField string
	calls       []callAsm
	finish      string
	usage       DeltaUsage
	model       string
	ttft        time.Duration
	size        int // assembled bytes so far
}

// Assembly bounds against a hostile or broken endpoint (ADR-102 butir 10).
const (
	maxResponseBytes = 16 << 20 // every byte the assembler keeps (PROV-M3)
	maxToolCalls     = 128
	// maxToolName bounds one assembled tool name (PROV-M1; OpenAI allows
	// 64 characters, MCP-prefixed names stay far below).
	maxToolName = 256
	// toolFragmentCost is charged per tool_calls fragment, so empty `{}`
	// fragments still consume the response budget (PROV-M2).
	toolFragmentCost = 64
)

func tooLarge() error { return &Error{Class: retry.Unclassified, Err: ErrResponseTooLarge} }

func (a *assembler) add(d *Delta) error {
	if a.size += len(d.Content) + a.reasoningCost(d); a.size > maxResponseBytes {
		return tooLarge()
	}
	a.content = append(a.content, d.Content...)
	if len(d.ReasoningField) > 0 && a.reasonField == "" {
		a.reasonField = string(d.ReasoningField)
	}
	switch {
	case d.ReasoningRaw != nil:
		if a.reasonParts == nil && len(a.reasonText) > 0 {
			a.reasonParts = append(a.reasonParts, quote(a.reasonText))
		}
		a.reasonParts = append(a.reasonParts, bytes.Clone(d.ReasoningRaw))
		a.reasonText = append(a.reasonText, partText(d.ReasoningRaw)...)
	case len(d.Reasoning) > 0:
		a.reasonText = append(a.reasonText, d.Reasoning...)
		if a.reasonParts != nil {
			a.reasonParts = append(a.reasonParts, quote(d.Reasoning))
		}
	}
	for i := range d.ToolCalls {
		t := &d.ToolCalls[i]
		if a.size += toolFragmentCost + len(t.ID) + len(t.Name) + len(t.Args); a.size > maxResponseBytes {
			return tooLarge()
		}
		if err := a.tool(t); err != nil {
			return err
		}
	}
	if len(d.Finish) > 0 {
		a.finish = string(d.Finish)
	}
	if d.Usage.Present {
		a.usage = d.Usage
	}
	if len(d.Model) > 0 {
		a.model = string(d.Model)
	}
	return nil
}

// reasoningCost is every byte d's reasoning makes the assembler keep
// (PROV-M3): the text, the stored part, and its share of the replay value
// built in result (quoted text, or the joined parts).
func (a *assembler) reasoningCost(d *Delta) int {
	switch {
	case d.ReasoningRaw != nil:
		n := 3 * len(d.ReasoningRaw) // part + joined copy + extracted text (≤ raw)
		if a.reasonParts == nil {
			n += 2 * quotedLen(a.reasonText) // earlier text becomes a quoted part
		}
		return n
	case len(d.Reasoning) > 0:
		n := len(d.Reasoning) + quotedLen(d.Reasoning) // text + replay value
		if a.reasonParts != nil {
			n += quotedLen(d.Reasoning) // stored quoted part
		}
		return n
	}
	return 0
}

// tool routes a fragment to its call (EC-PROV-001, ADR-102 butir 3):
// same id, else same index, else an id-less nameless fragment continues
// the last call, else a new call.
func (a *assembler) tool(t *ToolDelta) error {
	slot := a.slot(t)
	if slot < 0 {
		if len(a.calls) == maxToolCalls {
			return tooLarge()
		}
		a.calls = append(a.calls, callAsm{idx: t.Index, hasIdx: t.HasIndex, id: string(t.ID)})
		slot = len(a.calls) - 1
	}
	c := &a.calls[slot]
	if c.id == "" {
		c.id = string(t.ID)
	}
	if len(c.name)+len(t.Name) > maxToolName {
		return tooLarge()
	}
	c.name = append(c.name, t.Name...)
	c.args = append(c.args, t.Args...)
	return nil
}

// slot is the call t continues, or -1 for a new call.
func (a *assembler) slot(t *ToolDelta) int {
	slot := -1
	for i := range a.calls {
		c := &a.calls[i]
		if len(t.ID) > 0 && c.id == string(t.ID) || len(t.ID) == 0 && t.HasIndex && c.hasIdx && c.idx == t.Index {
			slot = i
		}
	}
	if slot < 0 && len(t.ID) == 0 && len(t.Name) == 0 && len(a.calls) > 0 {
		slot = len(a.calls) - 1
	}
	return slot
}

// result validates and returns the assembled response.
func (a *assembler) result() (Response, error) {
	if a.finish == "" {
		return Response{}, &Error{Class: retry.StreamBroken, Err: ErrStreamBroken}
	}
	r := Response{
		Content:      stripControl(string(a.content)),
		FinishReason: normalizeFinish(a.finish),
		Model:        a.model,
		TTFT:         a.ttft,
		Usage:        usage(a.usage),
	}
	for _, c := range a.calls {
		r.ToolCalls = append(r.ToolCalls, ToolCall{ID: c.id, Name: string(c.name), ArgumentsRaw: string(c.args)})
	}
	if len(a.reasonText) > 0 || a.reasonParts != nil {
		r.ReasoningText, r.ReasoningField = string(a.reasonText), a.reasonField
		r.Reasoning = quote(a.reasonText)
		if a.reasonParts != nil {
			r.Reasoning = joinParts(a.reasonParts)
		}
	}
	if r.FinishReason == FinishStop && strings.TrimSpace(r.Content) == "" && len(r.ToolCalls) == 0 {
		return r, &Error{Class: retry.EmptyResponse, Err: ErrEmptyResponse}
	}
	return r, nil
}

func usage(u DeltaUsage) budget.Usage {
	out := budget.Usage{Input: u.Prompt, CachedInput: u.Cached, Output: u.Output}
	if u.HasCost {
		out.CostUSD = &u.Cost
	}
	return out
}

// normalizeFinish maps provider synonyms to the SPEC set (ADR-102 butir 4).
func normalizeFinish(f string) string {
	switch f {
	case FinishToolCalls, "function_call", "tool_use":
		return FinishToolCalls
	case FinishLength, "max_tokens":
		return FinishLength
	case FinishContentFilter:
		return FinishContentFilter
	}
	return FinishStop
}

// controlTokens leak from some OpenAI-compatible servers (EC-PROV-013).
func controlTokens() []string {
	return []string{"<|im_end|>", "<|endoftext|>", "<|eot_id|>", "<|end|>", "</s>"}
}

// stripControl removes control tokens (and surrounding whitespace) only
// at the end of s, repeatedly; s is unchanged when there are none.
func stripControl(s string) string {
	const ws = " \t\r\n"
	orig := strings.TrimRight(s, ws)
	t := orig
	for {
		u := t
		for _, tok := range controlTokens() {
			u = strings.TrimRight(strings.TrimSuffix(u, tok), ws)
		}
		if u == t {
			break
		}
		t = u
	}
	if t == orig {
		return s
	}
	return t
}

func quote(b []byte) json.RawMessage {
	out, err := json.Marshal(string(b))
	if err != nil {
		return nil
	}
	return out
}

// joinParts builds the replay array once, byte-identical to the received
// parts (each already validated by the decoder), without re-encoding.
func joinParts(parts []json.RawMessage) json.RawMessage {
	n := 2 + len(parts)
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	out = append(out, '[')
	for i, p := range parts {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, p...)
	}
	return append(out, ']')
}

// quotedLen is an upper bound of len(quote(b)): encoding/json escapes
// quotes, backslashes and \n\r\t to 2 bytes, other controls, <, >, &,
// U+2028/U+2029 and invalid UTF-8 to 6 (\uXXXX).
func quotedLen(b []byte) int {
	n := 2
	for i := 0; i < len(b); {
		c := b[i]
		if c < utf8.RuneSelf {
			n += asciiQuoted(c)
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			n += 6
		case r == '\u2028' || r == '\u2029':
			n += 6
		default:
			n += size
		}
		i += size
	}
	return n
}

func asciiQuoted(c byte) int {
	switch {
	case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
		return 2
	case c < 0x20 || c == '<' || c == '>' || c == '&':
		return 6
	}
	return 1
}

// partText extracts display text from a non-string reasoning value: a
// string, or the text/thinking/content fields of objects, recursively in
// arrays (EC-PROV-006). Cold path.
func partText(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			b.WriteString(x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, k := range []string{"text", "thinking", "content"} {
				if s, ok := x[k].(string); ok {
					b.WriteString(s)
				}
			}
		}
	}
	walk(v)
	return b.String()
}
