package loop

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// callAsm accumulates one streamed tool call (EC-PROV-001).
type callAsm struct {
	idx       int
	hasIdx    bool
	id        string
	name      strings.Builder
	args      strings.Builder
	announced bool
}

// callBuilder routes fragments to calls with the same rules as the provider
// stream assembler (ADR-102): same id, else same index, else an id-less and
// nameless fragment continues the last call, else a new call.
type callBuilder struct {
	items []*callAsm
}

// add routes one batch of fragments and emits the start/args stream updates.
func (b *callBuilder) add(ds []ToolCallDelta, emit func(*event.Update)) {
	for _, d := range ds {
		slot := b.slot(d)
		if slot < 0 {
			b.items = append(b.items, &callAsm{idx: d.Index, hasIdx: d.HasIndex})
			slot = len(b.items) - 1
		}
		c := b.items[slot]
		if c.id == "" {
			c.id = d.ID
		}
		c.name.WriteString(d.Name)
		c.args.WriteString(d.Arguments)
		if !c.announced && c.name.Len() > 0 {
			c.announced = true
			emit(&event.Update{Kind: event.UpdateToolCallStart, ToolCallID: c.id, ToolName: sanitizeName(c.name.String())})
		}
		if d.Arguments != "" {
			emit(&event.Update{
				Kind: event.UpdateToolCallArgs, ToolCallID: c.id,
				ToolName: sanitizeName(c.name.String()), Text: d.Arguments,
			})
		}
	}
}

// slot is the call d continues, or -1 for a new call.
func (b *callBuilder) slot(d ToolCallDelta) int {
	slot := -1
	for i, c := range b.items {
		if d.ID != "" && c.id == d.ID || d.ID == "" && d.HasIndex && c.hasIdx && c.idx == d.Index {
			slot = i
		}
	}
	if slot < 0 && d.ID == "" && d.Name == "" && len(b.items) > 0 {
		slot = len(b.items) - 1
	}
	return slot
}

// calls returns the assembled calls. Arguments are not validated here; the
// dispatch path does that per call so one bad call does not hide the others.
func (b *callBuilder) calls() []ToolCall {
	if len(b.items) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(b.items))
	for _, c := range b.items {
		out = append(out, ToolCall{ID: c.id, Name: sanitizeName(c.name.String()), Arguments: c.args.String()})
	}
	return out
}

// normalizeArgs validates the assembled arguments without interpreting them
// (the Toolset owns the schema). Empty arguments mean an empty object. A
// duplicate key is rejected (TestJSONNoDuplicateKeys semantics, P3).
func normalizeArgs(args string) (string, *ToolError) {
	s := strings.TrimSpace(args)
	if s == "" {
		return "{}", nil
	}
	if !json.Valid([]byte(s)) {
		return args, &ToolError{
			Code: CodeInvalidArgs, Message: "arguments are not valid JSON",
			Hint: "send the arguments as one JSON object",
		}
	}
	if duplicateKey([]byte(s)) {
		return args, &ToolError{
			Code: CodeInvalidArgs, Message: "arguments contain a duplicate JSON key",
			Hint: "send each key once",
		}
	}
	return s, nil
}

// duplicateKey reports whether any JSON object in raw repeats a key.
func duplicateKey(raw []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var st keyState
	for {
		tok, err := dec.Token()
		if err != nil {
			return false // json.Valid already accepted the value
		}
		if delim, ok := tok.(json.Delim); ok {
			st.delim(delim)
			continue
		}
		if st.value(tok) {
			return true
		}
	}
}

// keyFrame is one open object or array.
type keyFrame struct {
	obj  bool
	keys map[string]bool
}

// keyState tracks object key expectations while scanning.
type keyState struct {
	frames    []keyFrame
	expectKey bool
}

func (s *keyState) delim(d json.Delim) {
	switch d {
	case '{':
		s.frames = append(s.frames, keyFrame{obj: true, keys: map[string]bool{}})
		s.expectKey = true
	case '[':
		s.frames = append(s.frames, keyFrame{})
		s.expectKey = false
	case '}', ']':
		s.frames = s.frames[:len(s.frames)-1]
		s.expectKey = s.topObject()
	}
}

// value consumes one scalar; a string in key position reports a duplicate.
func (s *keyState) value(tok any) bool {
	if s.expectKey && s.topObject() {
		if key, ok := tok.(string); ok {
			frame := &s.frames[len(s.frames)-1]
			if frame.keys[key] {
				return true
			}
			frame.keys[key] = true
		}
		s.expectKey = false
		return false
	}
	s.expectKey = s.topObject()
	return false
}

func (s *keyState) topObject() bool {
	return len(s.frames) > 0 && s.frames[len(s.frames)-1].obj
}
