package render

import (
	"html"
	"strings"
)

// Platform selects the wire format of a rendered reply (GAP-GW-04).
type Platform string

// Renderer platforms. Telegram stays on RenderTelegram (entities, no
// parse_mode); the others carry their native markup in Message.Text, with the
// Matrix HTML subset additionally in Message.HTML.
const (
	PlatformTelegram Platform = "telegram"
	PlatformSlack    Platform = "slack"
	PlatformWhatsApp Platform = "whatsapp"
	PlatformDiscord  Platform = "discord"
	PlatformMatrix   Platform = "matrix"
	PlatformPlain    Platform = "plain"
)

// Format parses src once and renders it for platform. An unknown
// platform falls back to plain text, and an over-complex document falls back
// to the escaped source (the F4 Telegram fallback, extended per renderer).
func Format(platform Platform, src string, lim Limits) []Message {
	doc, err := Parse(src)
	if err != nil {
		return []Message{{Text: Escape(src)}}
	}
	return ParseDoc(platform, doc, lim)
}

// ParseDoc renders an already parsed document. Callers that fan one reply out
// to several platforms parse once and pass the same AST (GAP-GW-04).
func ParseDoc(platform Platform, doc *Node, lim Limits) []Message {
	if doc == nil {
		return nil
	}
	if platform == PlatformTelegram {
		return chunk(renderDoc(doc), lim.norm())
	}
	return pack(platform, styledPieces(platform, doc), lim.norm())
}

type piece struct {
	text     string
	html     string
	body     string
	lang     string
	fence    bool
	isolated bool
}

func styledPieces(platform Platform, doc *Node) []piece {
	var out []piece
	for _, n := range doc.Kids {
		w := &styled{platform: platform}
		switch n.Kind {
		case CodeBlock:
			body := ""
			if len(n.Kids) > 0 {
				body = strings.TrimSuffix(n.Kids[0].Text, "\n")
			}
			lang := ""
			if f := strings.Fields(n.Info); len(f) > 0 {
				lang = f[0]
			}
			out = append(out, fencePieces(platform, lang, body)...)
		case List:
			w.list(n, 0)
			out = append(out, piece{text: w.text.String(), html: matrixHTML(platform, w.html.String())})
		case Table:
			w.table(n)
			out = append(out, piece{text: w.text.String(), html: matrixHTML(platform, w.html.String())})
		case Document, Paragraph, Item, Text, Code, Bold, Italic, Strike, Link, Row, Cell:
			w.inlines(n.Kids)
			out = append(out, piece{text: w.text.String(), html: matrixHTML(platform, w.html.String())})
		default:
			w.inlines(n.Kids)
			out = append(out, piece{text: w.text.String(), html: matrixHTML(platform, w.html.String())})
		}
	}
	return out
}

type styled struct {
	platform Platform
	text     strings.Builder
	html     strings.Builder
}

func (w *styled) newline() {
	w.text.WriteByte('\n')
	w.html.WriteByte('\n')
}

func (w *styled) raw(s string) {
	if w.platform == PlatformSlack {
		s = slackEscape(s)
	}
	w.text.WriteString(s)
	w.html.WriteString(html.EscapeString(s))
}

func (w *styled) inlines(ns []*Node) {
	for _, n := range ns {
		w.inline(n)
	}
}

func (w *styled) inline(n *Node) {
	switch n.Kind {
	case Text:
		w.raw(n.Text)
	case Code:
		w.code(n.Text)
	case Bold:
		w.markup(boldMark(w.platform), "b", n.Kids)
	case Italic:
		w.markup(italicMark(w.platform), "i", n.Kids)
	case Strike:
		w.markup(strikeMark(w.platform), "s", n.Kids)
	case Link:
		w.link(n.Text, inlineText(n.Kids))
	case Document, Paragraph, CodeBlock, List, Item, Table, Row, Cell:
	}
}

func (w *styled) markup(mark [2]string, tag string, kids []*Node) {
	w.text.WriteString(mark[0])
	w.html.WriteString("<" + tag + ">")
	w.inlines(kids)
	w.text.WriteString(mark[1])
	w.html.WriteString("</" + tag + ">")
}

func (w *styled) code(s string) {
	switch w.platform {
	case PlatformSlack, PlatformDiscord:
		w.text.WriteString(codeDelim(s, 1) + s + codeDelim(s, 1))
	case PlatformWhatsApp:
		w.text.WriteString(codeDelim(s, 3) + s + codeDelim(s, 3))
	case PlatformTelegram, PlatformMatrix, PlatformPlain:
		w.text.WriteString(s)
	default:
		w.text.WriteString(s)
	}
	w.html.WriteString("<code>" + html.EscapeString(s) + "</code>")
}

func (w *styled) link(url, label string) {
	switch w.platform {
	case PlatformSlack:
		w.text.WriteString("<" + slackEscape(url) + "|" + slackEscape(label) + ">")
	case PlatformDiscord:
		w.text.WriteString("[" + label + "](" + url + ")")
	case PlatformTelegram, PlatformWhatsApp, PlatformMatrix, PlatformPlain:
		w.text.WriteString(label + " (" + url + ")")
	default:
		w.text.WriteString(label + " (" + url + ")")
	}
	w.html.WriteString(`<a href="` + html.EscapeString(url) + `">` + html.EscapeString(label) + `</a>`)
}

func (w *styled) list(n *Node, depth int) {
	if w.platform == PlatformMatrix {
		w.html.WriteString("<ul>")
	}
	for i, it := range n.Kids {
		if i > 0 {
			w.newline()
		}
		if w.platform == PlatformMatrix {
			w.html.WriteString("<li>")
		}
		w.text.WriteString(strings.Repeat("  ", depth) + it.Text)
		for _, k := range it.Kids {
			if k.Kind == List {
				continue
			}
			w.inline(k)
		}
		if w.platform == PlatformMatrix {
			w.html.WriteString("</li>")
		}
		for _, k := range it.Kids {
			if k.Kind == List {
				w.newline()
				w.list(k, depth+1)
			}
		}
	}
	if w.platform == PlatformMatrix {
		w.html.WriteString("</ul>")
	}
}

func (w *styled) table(n *Node) {
	if w.platform == PlatformMatrix {
		w.html.WriteString("<table>")
	}
	for i, row := range n.Kids {
		if i > 0 {
			w.newline()
		}
		w.tableRow(row, i == 0)
		if i == 0 {
			w.tableSeparator(len(row.Kids))
		}
	}
	if w.platform == PlatformMatrix {
		w.html.WriteString("</table>")
	}
}

func (w *styled) tableRow(row *Node, header bool) {
	cellTag := "td"
	if header {
		cellTag = "th"
	}
	w.text.WriteString("|")
	if w.platform == PlatformMatrix {
		w.html.WriteString("<tr>")
	}
	for _, cell := range row.Kids {
		w.text.WriteString(" ")
		if w.platform == PlatformMatrix {
			w.html.WriteString("<" + cellTag + ">")
		}
		w.inlines(cell.Kids)
		w.text.WriteString(" |")
		if w.platform == PlatformMatrix {
			w.html.WriteString("</" + cellTag + ">")
		}
	}
	if w.platform == PlatformMatrix {
		w.html.WriteString("</tr>")
	}
}

func (w *styled) tableSeparator(cells int) {
	w.newline()
	w.text.WriteString("|")
	for range cells {
		w.text.WriteString(" --- |")
	}
}

func inlineText(ns []*Node) string {
	var b strings.Builder
	for _, n := range ns {
		b.WriteString(n.Text)
	}
	return b.String()
}

func slackEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func boldMark(p Platform) [2]string {
	switch p {
	case PlatformSlack, PlatformWhatsApp:
		return [2]string{"*", "*"}
	case PlatformDiscord:
		return [2]string{"**", "**"}
	case PlatformTelegram, PlatformMatrix, PlatformPlain:
		return [2]string{"", ""}
	}
	return [2]string{"", ""}
}

func italicMark(p Platform) [2]string {
	switch p {
	case PlatformSlack, PlatformWhatsApp:
		return [2]string{"_", "_"}
	case PlatformDiscord:
		return [2]string{"*", "*"}
	case PlatformTelegram, PlatformMatrix, PlatformPlain:
		return [2]string{"", ""}
	}
	return [2]string{"", ""}
}

func strikeMark(p Platform) [2]string {
	switch p {
	case PlatformSlack, PlatformWhatsApp:
		return [2]string{"~", "~"}
	case PlatformDiscord:
		return [2]string{"~~", "~~"}
	case PlatformTelegram, PlatformMatrix, PlatformPlain:
		return [2]string{"", ""}
	}
	return [2]string{"", ""}
}

func fencePieces(platform Platform, lang, body string) []piece {
	text, rendered := wrapFence(platform, lang, body)
	return []piece{{text: text, html: rendered, body: body, lang: lang, fence: true, isolated: platform != PlatformMatrix && platform != PlatformPlain}}
}

func wrapFence(platform Platform, lang, body string) (string, string) {
	switch platform {
	case PlatformPlain:
		return body, html.EscapeString(body)
	case PlatformMatrix:
		return body, matrixPre(lang, body)
	case PlatformTelegram, PlatformSlack, PlatformWhatsApp, PlatformDiscord:
		return fenceText(platform, lang, body), ""
	}
	return fenceText(platform, lang, body), ""
}

func fenceText(platform Platform, lang, body string) string {
	label := ""
	if platform == PlatformDiscord && lang != "" {
		label = lang
	}
	delim := codeDelim(body, 3)
	return delim + label + "\n" + body + "\n" + delim
}

// codeDelim returns n or more backticks exceeding every run in s, so neither a
// code span nor a fence can be closed by its own content.
func codeDelim(s string, n int) string {
	run, best := 0, 0
	for i := range len(s) {
		if s[i] == '`' {
			run++
			best = max(best, run)
			continue
		}
		run = 0
	}
	return strings.Repeat("`", max(n, best+1))
}

func matrixPre(lang, body string) string {
	class := ""
	if lang != "" {
		class = ` class="language-` + html.EscapeString(lang) + `"`
	}
	return "<pre><code" + class + ">" + html.EscapeString(body) + "</code></pre>"
}

func matrixHTML(platform Platform, s string) string {
	if platform == PlatformMatrix {
		return s
	}
	return ""
}
