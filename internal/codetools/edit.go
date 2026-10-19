package codetools

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// edit limits (CODE-TOOLS §5, CT-33).
const (
	maxEdits      = 50
	maxCandidates = 3
	maxAmbiguous  = 20
	sentinel      = "[REDACTED:" // redaction sentinel never written (ADR-071)
)

// EditItem is one edit_file edit: {old_string, new_string, replace_all} or {start_line, end_line, new_text} (CODE-TOOLS §5.2).
type EditItem struct {
	OldString  *string `json:"old_string,omitempty"`
	NewString  *string `json:"new_string,omitempty"`
	ReplaceAll bool    `json:"replace_all,omitempty"`
	StartLine  *int    `json:"start_line,omitempty"`
	EndLine    *int    `json:"end_line,omitempty"`
	NewText    *string `json:"new_text,omitempty"`
}

// doc is the logical view of a text file (CODE-TOOLS §5.3 butir 3): BOM
// stripped, UTF-16 decoded, every line's EOL recorded and shown as "\n".
// The mapping is lossless: render(doc) restores the input bytes.
type doc struct {
	enc   textEnc
	lines []docLine
	text  string // logical text: line contents joined with "\n"
	start []int  // logical offset of each line; start[len(lines)] = len(text)
}

type docLine struct {
	content string // without EOL
	eol     string // "\n", "\r\n" or "" (last line without EOL)
}

// parseDoc builds the logical view; files it cannot represent losslessly
// (odd-length UTF-16) are refused.
func parseDoc(raw []byte) (*doc, *ToolError) {
	enc := detectEnc(raw)
	if bin, desc := sniffBinary(raw, enc); bin {
		return nil, toolErr(CodeInvalidArgs, "binary files cannot be edited", "binary file (%s)", desc)
	}
	view := raw[len(enc.bom()):]
	if enc.enc != EncUTF8 {
		var odd []byte
		if view, odd = decodeUTF16(view, enc.enc == EncUTF16BE); odd != nil {
			return nil, toolErr(CodeInvalidArgs, "use write_file to rewrite it", "UTF-16 file with an odd byte count")
		}
	}
	d := &doc{enc: enc}
	var b strings.Builder
	b.Grow(len(view))
	for _, l := range splitLines(view) {
		dl := docLine{content: string(l)}
		switch {
		case bytes.HasSuffix(l, []byte("\r\n")):
			dl.content, dl.eol = dl.content[:len(l)-2], "\r\n"
		case bytes.HasSuffix(l, []byte("\n")):
			dl.content, dl.eol = dl.content[:len(l)-1], "\n"
		}
		d.start = append(d.start, b.Len())
		d.lines = append(d.lines, dl)
		b.WriteString(dl.content)
		if dl.eol != "" {
			b.WriteByte('\n')
		}
	}
	d.text = b.String()
	d.start = append(d.start, len(d.text))
	return d, nil
}

// render encodes lines back to file bytes (BOM, UTF-16 restored).
func (d *doc) render(lines []docLine) ([]byte, bool) {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l.content)
		b.WriteString(l.eol)
	}
	out := b.Bytes()
	if d.enc.enc != EncUTF8 {
		var ok bool
		if out, ok = encodeUTF16(out, d.enc.enc == EncUTF16BE); !ok {
			return nil, false
		}
	}
	return append([]byte(d.enc.bom()), out...), true
}

// lineAt is the 0-based line holding logical offset off.
func (d *doc) lineAt(off int) int {
	i, _ := slices.BinarySearch(d.start, off+1)
	return max(0, i-1)
}

// dominantEOL is the most frequent EOL ("\n" when none).
func (d *doc) dominantEOL() string {
	crlf, lf := 0, 0
	for _, l := range d.lines {
		switch l.eol {
		case "\r\n":
			crlf++
		case "\n":
			lf++
		}
	}
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}

type span struct {
	idx        int // edit index (0-based)
	start, end int // logical byte span [start, end)
	repl       string
}

// resolveEdits resolves every edit against the original text (order-independent) and rejects no-ops, overlaps and redaction sentinels (CODE-TOOLS §5.3 butir 4).
func resolveEdits(d *doc, rel string, edits []EditItem) ([]span, *ToolError) {
	if len(edits) == 0 || len(edits) > maxEdits {
		return nil, toolErr(CodeInvalidArgs, "", "/edits: 1..%d items", maxEdits)
	}
	var spans []span
	for i := range edits {
		s, err := resolveOne(d, rel, i, &edits[i])
		if err != nil {
			return nil, err
		}
		spans = append(spans, s...)
	}
	slices.SortStableFunc(spans, func(a, b span) int { return a.start - b.start })
	for k := 1; k < len(spans); k++ {
		p, c := spans[k-1], spans[k]
		if c.start < p.end || c.start == p.start {
			i, j := min(p.idx, c.idx)+1, max(p.idx, c.idx)+1
			return nil, toolErr(CodeInvalidArgs, "merge them into one edit", "edits %d and %d overlap at L%d", i, j, d.lineAt(c.start)+1)
		}
	}
	return spans, nil
}

// form validates the edit shape (§5.2: exactly one form) and the redaction sentinel guard (ADR-071); line reports the line-range form.
func (ed *EditItem) form(i int) (line bool, err *ToolError) {
	str := ed.OldString != nil || ed.NewString != nil || ed.ReplaceAll
	line = ed.StartLine != nil || ed.EndLine != nil || ed.NewText != nil
	strOK := ed.OldString != nil && ed.NewString != nil
	lineOK := ed.StartLine != nil && ed.EndLine != nil && ed.NewText != nil
	if str == line || str && !strOK || line && !lineOK {
		return line, toolErr(CodeInvalidArgs, "use {old_string,new_string[,replace_all]} or {start_line,end_line,new_text}", "/edits/%d: exactly one edit form required", i)
	}
	texts := []*string{ed.OldString, ed.NewString, ed.NewText}
	if slices.ContainsFunc(texts, func(s *string) bool { return s != nil && strings.Contains(*s, sentinel) }) {
		return line, toolErr(CodeInvalidArgs, "secret redacted; edit around it", "/edits/%d: contains a redaction sentinel", i)
	}
	return line, nil
}

func resolveOne(d *doc, rel string, i int, ed *EditItem) ([]span, *ToolError) {
	line, terr := ed.form(i)
	switch {
	case terr != nil:
		return nil, terr
	case line:
		return resolveLines(d, i, *ed.StartLine, *ed.EndLine, *ed.NewText)
	case *ed.OldString == "":
		return nil, toolErr(CodeInvalidArgs, "", "/edits/%d/old_string: must not be empty", i)
	case *ed.OldString == *ed.NewString:
		return nil, toolErr(CodeInvalidArgs, "", "/edits/%d: old_string equals new_string (no-op)", i)
	}
	old := *ed.OldString
	at := occurrences(d.text, old)
	switch {
	case len(at) == 0:
		return nil, noMatch(d, rel, i, old, *ed.NewString)
	case len(at) > 1 && !ed.ReplaceAll:
		var ls []string
		for _, a := range at[:min(len(at), maxAmbiguous)] {
			ls = append(ls, "L"+strconv.Itoa(d.lineAt(a)+1))
		}
		if len(at) > maxAmbiguous {
			ls = append(ls, "…")
		}
		return nil, toolErr(CodeAmbiguousMatch, "add surrounding lines or set replace_all", "edit %d: %d matches at %s", i+1, len(at), strings.Join(ls, ", "))
	}
	spans := make([]span, len(at))
	for k, a := range at {
		spans[k] = span{idx: i, start: a, end: a + len(old), repl: *ed.NewString}
	}
	return spans, nil
}

func occurrences(text, s string) []int {
	var at []int
	for off := 0; ; {
		k := strings.Index(text[off:], s)
		if k < 0 {
			return at
		}
		at = append(at, off+k)
		off += k + len(s)
	}
}

// resolveLines maps a 1-based line range (negative from the end) to a span; one trailing "\n" is the last terminator, "" deletes the lines.
func resolveLines(d *doc, i, s, e int, text string) ([]span, *ToolError) {
	n := len(d.lines)
	if s < 0 && e < 0 {
		s, e = n+s+1, n+e+1
	}
	if s < 1 || e < s || e > n {
		return nil, toolErr(CodeInvalidArgs, "read_file shows the line numbers", "/edits/%d: line range %d..%d outside 1..%d (both positive or both negative)", i, s, e, n)
	}
	start, end := d.start[s-1], d.start[e]
	if text != "" {
		text = strings.TrimSuffix(text, "\n")
		if d.lines[e-1].eol != "" {
			text += "\n"
		}
	}
	if text == d.text[start:end] {
		return nil, toolErr(CodeInvalidArgs, "", "/edits/%d: new_text equals lines %d..%d (no-op)", i, s, e)
	}
	return []span{{idx: i, start: start, end: end, repl: text}}, nil
}

// apply rewrites the lines the spans touch; outside bytes are kept and "\n" takes the region's first EOL (fallback: dominant) (CODE-TOOLS §5.3 butir 5).
func (d *doc) apply(spans []span) []docLine {
	out := make([]docLine, 0, len(d.lines)+8)
	next := 0 // first line not yet copied
	for k := 0; k < len(spans); {
		sl := d.lineAt(spans[k].start)
		el := d.lineAt(max(spans[k].start, spans[k].end-1))
		j := k + 1
		for j < len(spans) && d.lineAt(spans[j].start) <= el { // same region
			el = max(el, d.lineAt(max(spans[j].start, spans[j].end-1)))
			j++
		}
		out = append(out, d.lines[next:sl]...)
		var b strings.Builder
		pos := d.start[sl]
		for _, sp := range spans[k:j] {
			b.WriteString(d.text[pos:sp.start])
			b.WriteString(sp.repl)
			pos = sp.end
		}
		b.WriteString(d.text[pos:d.start[el+1]])
		eol := d.lines[sl].eol
		if eol == "" {
			eol = d.dominantEOL()
		}
		out = append(out, splitDocLines(b.String(), eol)...)
		next, k = el+1, j
	}
	return append(out, d.lines[next:]...)
}

// splitDocLines splits logical text into lines terminated by eol.
func splitDocLines(s, eol string) []docLine {
	var out []docLine
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, docLine{content: s})
			break
		}
		out = append(out, docLine{content: s[:i], eol: eol})
		s = s[i+1:]
	}
	return out
}

// noMatch reports ≤ maxCandidates nearest windows as information only; they are never applied (P4, ADR-005).
func noMatch(d *doc, rel string, i int, old, repl string) *ToolError {
	te := toolErr(CodeNoMatch, "", "edit %d: old_string not found in %s", i+1, rel)
	oldLines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	n := len(oldLines)
	var hints []string
	for _, c := range candidates(d, oldLines) {
		win := d.text[d.start[c.from]:d.start[c.from+n]]
		hints = append(hints, fmt.Sprintf("L%d-%d %s", c.from+1, c.from+n, reason(strings.TrimSuffix(win, "\n"), strings.TrimSuffix(old, "\n"), c.score, n)))
	}
	if repl != "" && strings.Contains(d.text, repl) {
		hints = append(hints, fmt.Sprintf("new_string already present at L%d", d.lineAt(strings.Index(d.text, repl))+1))
	}
	if len(hints) > 0 {
		te.Hint = "nearest (NOT applied): " + strings.Join(hints, "; ")
	} else if !utf8.ValidString(d.text) {
		te.Hint = "lines with invalid bytes can only be edited by line range"
	}
	return te
}

type candidate struct{ from, score int }

// candidates are the ≤ maxCandidates windows anchored on old's first or last line, scored by contained old lines (lineKey; no edit distance).
func candidates(d *doc, oldLines []string) []candidate {
	want := map[string]bool{}
	for _, l := range oldLines {
		want[lineKey(l)] = true
	}
	n := len(oldLines)
	first, last := lineKey(oldLines[0]), lineKey(oldLines[n-1])
	var cs []candidate
	seen := map[int]bool{}
	add := func(from int) {
		if from < 0 || from+n > len(d.lines) || seen[from] {
			return
		}
		seen[from] = true
		c := candidate{from: from}
		for _, w := range d.lines[from : from+n] {
			if want[lineKey(w.content)] {
				c.score++
			}
		}
		cs = append(cs, c)
	}
	for li, l := range d.lines {
		k := lineKey(l.content)
		if k == first {
			add(li)
		}
		if k == last {
			add(li - n + 1)
		}
	}
	slices.SortStableFunc(cs, func(a, b candidate) int { return b.score - a.score })
	return cs[:min(len(cs), maxCandidates)]
}

// lineKey compares lines for candidate search only: trimmed, inner whitespace collapsed, NFC; it never decides what an edit changes.
func lineKey(l string) string { return norm.NFC.String(strings.Join(strings.Fields(l), " ")) }

func reason(win, old string, score, n int) string {
	squash := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	switch {
	case squash(win) == squash(old) && strings.Contains(win, "\t") != strings.Contains(old, "\t"):
		return "whitespace differs (tab vs spaces)"
	case squash(win) == squash(old):
		return "whitespace differs"
	case norm.NFC.String(win) == norm.NFC.String(old):
		return "unicode NFC vs NFD"
	case strings.NewReplacer(`\"`, `"`, `\'`, `'`).Replace(win) == strings.NewReplacer(`\"`, `"`, `\'`, `'`).Replace(old):
		return "escaped quotes differ"
	}
	return fmt.Sprintf("%d/%d lines similar", score, n)
}
