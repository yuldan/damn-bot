package render

import "strings"

const (
	// maxNodes bounds the AST; scans keeps closer searching linear.
	maxNodes = 20000
	specials = "`*_["
)

type budget struct{ nodes, scans int }

func (b *budget) scan(n int) bool { b.scans -= n; return b.scans >= 0 }

type parser struct {
	lines []string
	i     int
	b     *budget
}

// Parse parses the supported markdown subset into an AST of escaped text.
func Parse(src string) (*Node, error) {
	text := Escape(src)
	p := &parser{splitLines(text), 0, &budget{maxNodes, 16*len(text) + 1024}}
	doc := &Node{Kind: Document}
	for p.i < len(p.lines) {
		p.b.nodes--
		if p.b.nodes < 0 || p.b.scans <= 0 {
			return nil, ErrTooComplex
		}
		if strings.TrimSpace(p.lines[p.i]) == "" {
			p.i++
			continue
		}
		n, err := p.block()
		if err != nil {
			return nil, err
		}
		if n.Kind != CodeBlock || n.Kids[0].Text != "" {
			doc.Kids = append(doc.Kids, n)
		}
	}
	return doc, nil
}

func (p *parser) block() (*Node, error) {
	if f, ok := opener(p.lines[p.i]); ok {
		return p.fenced(f), nil
	}
	if _, _, ok := marker(p.lines[p.i]); ok {
		return p.list()
	}
	return p.paragraph()
}

func (p *parser) paragraph() (*Node, error) {
	var b strings.Builder
	for p.i < len(p.lines) {
		s := p.lines[p.i]
		if strings.TrimSpace(s) == "" {
			break
		}
		if _, f := opener(s); f {
			break
		}
		if _, _, l := marker(s); l {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
		p.i++
	}
	kids, err := inlines(b.String(), p.b)
	return &Node{Kind: Paragraph, Kids: kids}, err
}

// list reads simple marker lines; an indented line continues the item.
func (p *parser) list() (*Node, error) {
	n := &Node{Kind: List}
	for p.i < len(p.lines) {
		mark, rest, ok := marker(p.lines[p.i])
		if !ok {
			break
		}
		p.i++
		var b strings.Builder
		b.WriteString(rest)
		for p.i < len(p.lines) && p.lines[p.i] != "" && (p.lines[p.i][0] == ' ' || p.lines[p.i][0] == '\t') {
			if _, _, nested := marker(p.lines[p.i]); nested {
				break
			}
			b.WriteByte('\n')
			b.WriteString(strings.TrimSpace(p.lines[p.i]))
			p.i++
		}
		kids, err := inlines(b.String(), p.b)
		if err != nil {
			return nil, err
		}
		n.Kids = append(n.Kids, &Node{Kind: Item, Text: mark, Kids: kids})
	}
	return n, nil
}

// fenced consumes a code block; an unclosed fence runs to EOF.
func (p *parser) fenced(f fence) *Node {
	p.i++
	var b strings.Builder
	for p.i < len(p.lines) && !f.closes(p.lines[p.i]) {
		b.WriteString(p.lines[p.i] + "\n")
		p.i++
	}
	p.i++
	return &Node{Kind: CodeBlock, Info: f.info, Kids: []*Node{{Kind: Text, Text: b.String()}}}
}

// splitLines strips the CR of CRLF pairs (EC-PLAT-009).
func splitLines(src string) []string {
	if src == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

type fence struct {
	ch   byte
	n    int
	info string
}

// opener recognises a fence opener indented at most three spaces.
func opener(s string) (fence, bool) {
	t := strings.TrimLeft(s, " ")
	if len(s)-len(t) > 3 || t == "" || (t[0] != '`' && t[0] != '~') {
		return fence{}, false
	}
	n := run(t, 0)
	info := strings.TrimSpace(t[n:])
	if n < 3 || (t[0] == '`' && strings.ContainsRune(info, '`')) {
		return fence{}, false
	}
	return fence{t[0], n, info}, true
}

// closes reports a fence closer of the same character.
func (f fence) closes(s string) bool {
	t := strings.TrimLeft(s, " ")
	n := 0
	for n < len(t) && t[n] == f.ch {
		n++
	}
	return len(s)-len(t) <= 3 && n >= f.n && strings.TrimSpace(t[n:]) == ""
}

// marker recognises "- ", "* ", "+ " and "<digits>[.)] " prefixes.
func marker(s string) (mark, rest string, ok bool) {
	t := strings.TrimLeft(s, " ")
	if len(s)-len(t) > 3 || t == "" {
		return "", "", false
	}
	if strings.ContainsRune("-*+", rune(t[0])) && len(t) > 1 && t[1] == ' ' {
		return t[:1] + " ", strings.TrimLeft(t[2:], " "), true
	}
	return ordered(t)
}

func ordered(t string) (mark, rest string, ok bool) {
	i := 0
	for i < len(t) && i < 9 && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(t) || (t[i] != '.' && t[i] != ')') || t[i+1] != ' ' {
		return "", "", false
	}
	return t[:i+1] + " ", strings.TrimLeft(t[i+2:], " "), true
}

// inlines parses one inline run; emphasis labels stay flat text.
func inlines(s string, b *budget) ([]*Node, error) {
	var out []*Node
	for pos := 0; pos < len(s); {
		if b.nodes <= 0 || b.scans <= 0 {
			return nil, ErrTooComplex
		}
		i := strings.IndexAny(s[pos:], specials)
		if i != 0 {
			if i < 0 {
				i = len(s) - pos
			}
			b.nodes--
			out = append(out, &Node{Kind: Text, Text: s[pos : pos+i]})
			pos += i
			continue
		}
		var n *Node
		var next int
		var ok bool
		switch s[pos] {
		case '`':
			n, next, ok = codeSpan(s, pos, b)
		case '*', '_':
			n, next, ok = emphasis(s, pos, b)
		case '[':
			n, next, ok = link(s, pos, b)
		}
		if !ok {
			n, next = &Node{Kind: Text, Text: s[pos : pos+1]}, pos+1
		}
		b.nodes--
		out = append(out, n)
		pos = next
	}
	return out, nil
}

func run(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == s[i] {
		n++
	}
	return n
}

// codeSpan parses a backtick run closed by a run of the same length.
func codeSpan(s string, pos int, b *budget) (*Node, int, bool) {
	n, rest := run(s, pos), s[pos+run(s, pos):]
	mark := strings.Repeat("`", n)
	for off := 0; ; {
		j := strings.Index(rest[off:], mark)
		if j < 0 || !b.scan(j+n) {
			return nil, pos, false
		}
		off += j
		if m := run(rest, off); m != n {
			off += m
			continue
		}
		text := rest[:off]
		if len(text) >= 2 && text[0] == ' ' && text[len(text)-1] == ' ' && strings.TrimSpace(text) != "" {
			text = text[1 : len(text)-1]
		}
		return &Node{Kind: Code, Text: text}, pos + n + off + n, true
	}
}

// emphasis parses **strong** and *italic* (and underscore variants).
func emphasis(s string, pos int, b *budget) (*Node, int, bool) {
	width := 1
	if run(s, pos) >= 2 {
		width = 2
	}
	start := pos + width
	if start >= len(s) || s[start] == ' ' || s[start] == '\n' {
		return nil, pos, false
	}
	end := closer(s, start, strings.Repeat(s[pos:pos+1], width), b)
	if end < 0 {
		return nil, pos, false
	}
	kind := Italic
	if width == 2 {
		kind = Bold
	}
	return &Node{Kind: kind, Kids: []*Node{{Kind: Text, Text: s[start:end]}}}, end + width, true
}

func closer(s string, from int, mark string, b *budget) int {
	for i := from; i+len(mark) <= len(s); i += len(mark) {
		j := strings.Index(s[i:], mark)
		if j < 0 || !b.scan(j+len(mark)) {
			return -1
		}
		i += j
		if s[i-1] != ' ' && s[i-1] != '\n' {
			return i
		}
	}
	return -1
}

// link parses `[label](url)` with an http, https or tg URL.
func link(s string, pos int, b *budget) (*Node, int, bool) {
	l := strings.IndexByte(s[pos+1:], ']')
	if l < 0 || !b.scan(l+1) {
		return nil, pos, false
	}
	l += pos + 1
	if l+2 >= len(s) || s[l+1] != '(' {
		return nil, pos, false
	}
	r := strings.IndexByte(s[l+2:], ')')
	if r < 0 || !b.scan(r+1) {
		return nil, pos, false
	}
	r += l + 2
	url := s[l+2 : r]
	if !safeURL(url) {
		return nil, pos, false
	}
	return &Node{Kind: Link, Text: url, Kids: []*Node{{Kind: Text, Text: s[pos+1 : l]}}}, r + 1, true
}

func safeURL(u string) bool {
	if u == "" || strings.ContainsAny(u, " \t\r\n<>\"") {
		return false
	}
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "tg://")
}
