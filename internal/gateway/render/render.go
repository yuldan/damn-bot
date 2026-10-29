// Package render parses markdown once into a light AST and renders Telegram
// plain text plus entities (GAP-GW-04), in UTF-16 units (EC-PLAT-011).
package render

import (
	"cmp"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxLen is the Bot API message limit in UTF-16 units (EC-PLAT-011).
const (
	MaxLen           = 4096
	DefaultMaxChunks = 8
	Truncated        = "[truncated]"
)

// ErrTooComplex is answered by RenderTelegram with the plain fallback.
var ErrTooComplex = errors.New("render: markdown too complex")

// Entity is one Bot API entity; Offset and Length are UTF-16 units.
type Entity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	Language string `json:"language,omitempty"`
}

// Entity types emitted by the renderer.
const (
	EntityBold     = "bold"
	EntityItalic   = "italic"
	EntityCode     = "code"
	EntityPre      = "pre"
	EntityTextLink = "text_link"
)

// Message is one send: plain text plus entities, with no parse_mode.
type Message struct {
	Text     string
	Entities []Entity
}

// Limits bound one reply; zero means MaxLen and DefaultMaxChunks.
type Limits struct{ MaxLen, MaxChunks int }

func (l Limits) norm() Limits {
	if l.MaxLen <= 0 {
		l.MaxLen = MaxLen
	}
	l.MaxLen = min(max(l.MaxLen, Units(Truncated)+1), MaxLen) // marker must fit
	if l.MaxChunks <= 0 {
		l.MaxChunks = DefaultMaxChunks
	}
	return l
}

// Kind is an AST node kind; CodeBlock keeps Info and its body in Kids[0].Text.
type Kind uint8

// Document is the AST root; the other kinds classify blocks and inlines.
const (
	Document Kind = iota
	Paragraph
	CodeBlock
	List
	Item
	Text
	Code
	Bold
	Italic
	Link
)

// Node is one AST node.
type Node struct {
	Kind Kind
	Info string
	Text string
	Kids []*Node
}

// Escape makes user text displayable: invalid UTF-8 becomes U+FFFD and odd
// controls become \xNN (no parse_mode is sent, so no markup escaping applies).
func Escape(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		if !isControl(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteString(`\x`)
		b.WriteByte(hex[r>>4])
		b.WriteByte(hex[r&0xf])
	}
	return b.String()
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// Units is the UTF-16 length of s (EC-PLAT-011).
func Units(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// ParseAnchor validates a reply anchor, a Telegram message id; on ok=false the
// caller sends without reply_to_message_id (EC-PLAT-018).
func ParseAnchor(raw string) (id int64, ok bool) {
	if raw == "" || raw[0] < '1' || raw[0] > '9' {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n > 1<<31-1 {
		return 0, false
	}
	return n, true
}

// RenderTelegram returns the messages to send, chunked to Limits.MaxLen.
func RenderTelegram(src string, lim Limits) []Message { //nolint:revive // GAP-GW-04 names this API and its fuzz target
	return chunk(render(src), lim.norm())
}

// MeasureCard is the UTF-16 length after parsing and escaping (EC-PLAT-012).
func MeasureCard(src string) int { return Units(render(src).Text) }

func render(src string) Message {
	doc, err := Parse(src)
	if err != nil {
		return Message{Text: Escape(src)}
	}
	var w writer
	sep := ""
	for _, n := range doc.Kids {
		w.write(sep)
		w.node(n)
		sep = "\n\n"
	}
	return Message{Text: w.b.String(), Entities: sortEntities(w.ents)}
}

type writer struct {
	b     strings.Builder
	ents  []Entity
	units int
}

func (w *writer) write(s string) {
	if s != "" {
		w.b.WriteString(s)
		w.units += Units(s)
	}
}

func (w *writer) span(typ, url, lang string, fn func()) {
	start := w.units
	fn()
	if n := w.units - start; n > 0 { // the Bot API rejects zero-length entities
		w.ents = append(w.ents, Entity{Type: typ, Offset: start, Length: n, URL: url, Language: lang})
	}
}

func (w *writer) nodes(ns []*Node) {
	for _, n := range ns {
		w.node(n)
	}
}

func (w *writer) node(n *Node) {
	switch n.Kind {
	case Document, Paragraph:
		w.nodes(n.Kids)
	case Text:
		w.write(n.Text)
	case Code:
		w.span(EntityCode, "", "", func() { w.write(n.Text) })
	case Bold, Italic:
		typ := EntityBold
		if n.Kind == Italic {
			typ = EntityItalic
		}
		w.span(typ, "", "", func() { w.nodes(n.Kids) })
	case Link:
		w.span(EntityTextLink, n.Text, "", func() { w.nodes(n.Kids) })
	case CodeBlock:
		lang := ""
		if f := strings.Fields(n.Info); len(f) > 0 {
			lang = f[0]
		}
		w.span(EntityPre, "", lang, func() { w.nodes(n.Kids) })
	case List:
		for i, it := range n.Kids {
			if i > 0 {
				w.write("\n")
			}
			w.write(it.Text)
			w.nodes(it.Kids)
		}
	case Item: // rendered by the enclosing List
	}
}

func chunk(msg Message, lim Limits) []Message {
	var out []Message
	for msg.Text != "" {
		text, units := cut(msg.Text, lim.MaxLen)
		out = append(out, Message{Text: text, Entities: sliceEntities(msg.Entities, 0, units)})
		msg.Text, msg.Entities = msg.Text[len(text):], sliceEntities(msg.Entities, units, math.MaxInt)
	}
	if len(out) > lim.MaxChunks {
		out = out[:lim.MaxChunks]
		truncate(&out[len(out)-1], lim.MaxLen)
	}
	return out
}

func cut(s string, maxUnits int) (string, int) {
	units, end := 0, 0
	for end < len(s) {
		r, w := utf8.DecodeRuneInString(s[end:])
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > maxUnits {
			break
		}
		units += n
		end += w
	}
	if end == len(s) {
		return s, units
	}
	if j := strings.LastIndexByte(s[:end], '\n'); j >= 0 {
		end = j + 1
	} else if j := strings.LastIndexByte(s[:end], ' '); j >= 0 {
		end = j + 1
	}
	return s[:end], Units(s[:end])
}

func sortEntities(ents []Entity) []Entity {
	if len(ents) > 1 {
		slices.SortFunc(ents, func(a, b Entity) int {
			return cmp.Or(cmp.Compare(a.Offset, b.Offset), cmp.Compare(b.Length, a.Length), strings.Compare(a.Type, b.Type))
		})
	}
	return ents
}

func truncate(m *Message, maxUnits int) {
	text, units := cut(m.Text, maxUnits-Units(Truncated))
	m.Text = text + Truncated
	m.Entities = sliceEntities(m.Entities, 0, units)
}

func sliceEntities(ents []Entity, from, to int) []Entity {
	var out []Entity
	for _, e := range ents {
		lo, hi := max(e.Offset, from), min(e.Offset+e.Length, to)
		if hi > lo {
			e.Offset, e.Length = lo-from, hi-lo
			out = append(out, e)
		}
	}
	return out
}
