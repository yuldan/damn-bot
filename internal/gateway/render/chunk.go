package render

import "strings"

// pack turns styled pieces into messages no longer than lim.MaxLen, never
// splitting a fenced block without reopening the fence (GAP-GW-04). Blocks
// are the preferred boundary; a block longer than one message is split at a
// newline or space, and a code block is re-wrapped per piece.
func pack(platform Platform, pieces []piece, lim Limits) []Message {
	p := &packer{platform: platform, lim: lim}
	for _, pc := range pieces {
		p.add(pc)
	}
	p.flush()
	return p.finish()
}

type packer struct {
	platform Platform
	lim      Limits
	msgs     []Message
	cur      strings.Builder
	curHTML  strings.Builder
	curUnits int
}

func (p *packer) add(pc piece) {
	if pc.text == "" {
		return
	}
	if pc.fence {
		p.flush()
		for _, q := range splitFence(p.platform, pc, p.lim.MaxLen) {
			p.msgs = append(p.msgs, Message{Text: q.text, HTML: q.html})
		}
		return
	}
	if pc.isolated {
		p.flush()
	}
	if p.curUnits > 0 && p.curUnits+2+Units(pc.text) > p.lim.MaxLen {
		p.flush()
	}
	if Units(pc.text) <= p.lim.MaxLen {
		p.write(pc.text, pc.html)
		return
	}
	p.splitLong(pc)
}

func (p *packer) splitLong(pc piece) {
	for _, part := range splitText(pc.text, p.lim.MaxLen) {
		if part == "" {
			continue
		}
		if p.curUnits > 0 && p.curUnits+2+Units(part) > p.lim.MaxLen {
			p.flush()
		}
		rendered := ""
		if p.platform == PlatformMatrix {
			rendered = EscapeHTML(part)
		}
		p.write(part, rendered)
	}
}

func (p *packer) write(text, rendered string) {
	if p.curUnits > 0 {
		p.cur.WriteString("\n\n")
		if p.curHTML.Len() > 0 {
			p.curHTML.WriteString("\n")
		}
		p.curUnits += 2
	}
	p.cur.WriteString(text)
	p.curHTML.WriteString(rendered)
	p.curUnits += Units(text)
}

func (p *packer) flush() {
	if p.cur.Len() == 0 {
		return
	}
	p.msgs = append(p.msgs, Message{Text: p.cur.String(), HTML: p.curHTML.String()})
	p.cur.Reset()
	p.curHTML.Reset()
	p.curUnits = 0
}

func (p *packer) finish() []Message {
	if len(p.msgs) > p.lim.MaxChunks {
		p.msgs = p.msgs[:p.lim.MaxChunks]
		truncate(&p.msgs[len(p.msgs)-1], p.lim.MaxLen)
	}
	if p.platform != PlatformMatrix {
		return p.msgs
	}
	for i := range p.msgs {
		if p.msgs[i].HTML == "" {
			p.msgs[i].HTML = EscapeHTML(p.msgs[i].Text)
		}
	}
	return p.msgs
}

// splitFence splits one code block, re-wrapping every piece in the same fence
// (and language). A piece never contains an unbalanced fence.
func splitFence(platform Platform, p piece, maxLen int) []piece {
	if Units(p.text) <= maxLen {
		return []piece{{text: p.text, html: p.html}}
	}
	budget := maxLen
	if platform != PlatformMatrix && platform != PlatformPlain {
		open, _ := wrapFence(platform, p.lang, "")
		budget -= Units(open)
	}
	var out []piece
	for _, body := range splitText(p.body, max(budget, 1)) {
		body = strings.TrimSuffix(body, "\n")
		if body == "" {
			continue
		}
		text, rendered := wrapFence(platform, p.lang, body)
		out = append(out, piece{text: text, html: rendered})
	}
	return out
}

// splitText cuts s into pieces of at most maxLen UTF-16 units, preferring a
// newline and then a space; only a token longer than maxLen is hard-cut.
func splitText(s string, maxLen int) []string {
	var out []string
	for s != "" {
		part, _ := cut(s, maxLen)
		if part == "" {
			_, size := runeAt(s)
			part = s[:size]
		}
		out = append(out, part)
		s = s[len(part):]
	}
	return out
}

func runeAt(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 1
}
