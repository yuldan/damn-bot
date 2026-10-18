package codetools

import (
	"bytes"
	"strings"
)

// edit limits (CODE-TOOLS §5, CT-33).
const (
	sentinel = "[REDACTED:" // redaction sentinel never written (ADR-071)
)

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
