package loop

import (
	"strings"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// streamCollector turns one provider attempt's deltas into stream updates
// and assembles its tool calls. It owns the redaction buffers of that
// attempt, so a retry never reuses held bytes.
type streamCollector struct {
	t    *turn
	b    callBuilder
	text *deltaBuf
}

// newStreamCollector wires the redaction buffers of ADR-081/LP8-19.
func newStreamCollector(t *turn) *streamCollector {
	c := &streamCollector{t: t, text: t.newDeltaBuf()}
	c.b.newBuf = t.newDeltaBuf
	return c
}

// emit handles one provider delta.
func (c *streamCollector) emit(d Delta) {
	if d.Text != "" {
		if out := c.text.push(d.Text); out != "" {
			c.t.emit(&event.Update{Kind: event.UpdateTextDelta, Text: out})
		}
	}
	c.b.add(d.ToolCalls, func(u *event.Update) { c.t.emit(u) })
}

// flush decides the held tails at the assembly boundary and returns the
// assembled calls.
func (c *streamCollector) flush() []ToolCall {
	emit := func(u *event.Update) { c.t.emit(u) }
	if out := c.text.flush(c.t.redact); out != "" {
		c.t.emit(&event.Update{Kind: event.UpdateTextDelta, Text: out})
	}
	c.b.flushArgs(emit, c.t.redact)
	return c.b.calls()
}

// deltaBuf holds one streamed text or tool-argument field until the
// redactor can decide it safely (LP8-19). A nil buffer means no Redactor is
// configured, so the delta itself is final and is emitted unchanged.
type deltaBuf struct {
	filter StreamFilter
	held   strings.Builder
}

// newDeltaBuf returns the buffer for one field. With a Redactor but no
// cross-delta filter the whole field is held, because a secret may be split
// across deltas and per-delta redaction cannot see it.
func (t *turn) newDeltaBuf() *deltaBuf {
	if t.deps.Redactor == nil {
		return nil
	}
	b := &deltaBuf{}
	if t.deps.NewRedactStream != nil {
		b.filter = t.deps.NewRedactStream()
	}
	return b
}

// push returns the text that may be emitted for this chunk.
func (b *deltaBuf) push(s string) string {
	if b == nil {
		return s
	}
	if b.filter != nil {
		return string(b.filter.Push(nil, []byte(s)))
	}
	b.held.WriteString(s)
	return ""
}

// flush returns the field's remaining text after assembly.
func (b *deltaBuf) flush(redact func(string) string) string {
	if b == nil {
		return ""
	}
	if b.filter != nil {
		return string(b.filter.Flush(nil))
	}
	if b.held.Len() == 0 {
		return ""
	}
	s := b.held.String()
	b.held.Reset()
	return redact(s)
}
