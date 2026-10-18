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
}

type docLine struct {
	content string // without EOL
	eol     string // "\n", "\r\n" or "" (last line without EOL)
}

// render encodes lines back to file bytes (BOM, UTF-16 restored).
func (d *doc) render(lines []docLine) ([]byte, bool) {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l.content)
		b.WriteString(l.eol)
	}
	if d.enc.enc != EncUTF8 {
		return nil, false
	}
	return append([]byte(d.enc.bom()), b.Bytes()...), true
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
