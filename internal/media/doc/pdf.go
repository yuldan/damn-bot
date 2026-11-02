package doc

import (
	"bytes"
	"compress/lzw"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

const maxCMapEntries = 1 << 16

var errSkipStream = errors.New("media/doc: PDF stream skipped")

type cmapTable struct {
	m     map[uint32]string
	width int
}

func readPDF(data []byte) (Document, error) {
	if bytes.Contains(data, []byte("/Encrypt")) {
		return Document{}, fail(CodeUnsupported, "encrypted PDF", "")
	}
	streams, cmaps, err := pdfStreams(data)
	if err != nil {
		return Document{}, err
	}
	c := &cmapTable{m: map[uint32]string{}}
	for _, raw := range cmaps {
		parseCMap(raw, c)
	}
	var out strings.Builder
	for _, s := range streams {
		if t := pdfText(s, c); strings.TrimSpace(t) != "" {
			out.WriteString(t)
			out.WriteByte('\n')
			if out.Len() > MaxOutputBytes {
				break
			}
		}
	}
	if strings.TrimSpace(out.String()) == "" {
		return Document{}, fail(CodeNoTextLayer, "PDF has no text layer", "try vision_analyze for a scan")
	}
	pages := bytes.Count(data, []byte("/Type /Page")) - bytes.Count(data, []byte("/Type /Pages"))
	return Document{Markdown: out.String(), Pages: pages}, nil
}

func pdfStreams(raw []byte) ([][]byte, [][]byte, error) {
	var streams, cmaps [][]byte
	pos, total := 0, int64(0)
	for {
		start, end, next, dict, ok := nextPDFStream(raw, pos)
		if !ok {
			return streams, cmaps, nil
		}
		pos = next
		dec, err := decodeStream(raw[start:end], dict, &total)
		if errors.Is(err, errSkipStream) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if len(streams)+len(cmaps) >= maxPDFStreams {
			return nil, nil, fail(CodeTooLarge, "PDF has more than 5000 streams", "")
		}
		switch {
		case isCMap(dec):
			cmaps = append(cmaps, dec)
		case looksContent(dec):
			streams = append(streams, dec)
		}
	}
}

func nextPDFStream(raw []byte, pos int) (int, int, int, []byte, bool) {
	for {
		i := bytes.Index(raw[pos:], []byte("stream"))
		if i < 0 {
			return 0, 0, 0, nil, false
		}
		i += pos
		if i > 0 && isLetter(raw[i-1]) {
			pos = i + 6
			continue
		}
		start := i + 6
		if start < len(raw) && raw[start] == '\r' {
			start++
		}
		if start < len(raw) && raw[start] == '\n' {
			start++
		}
		dict := dictOf(raw, i)
		end, next := streamEnd(raw, start, dict)
		if end < 0 {
			pos = i + 6
			continue
		}
		return start, end, next, dict, true
	}
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func dictOf(raw []byte, i int) []byte {
	start := bytes.LastIndex(raw[:i], []byte("endobj"))
	if start >= 0 {
		start += 6
	} else {
		start = 0
	}
	if i-start > 4096 {
		start = i - 4096
	}
	return raw[start:i]
}

func streamEnd(raw []byte, start int, dict []byte) (int, int) {
	if n, ok := pdfLength(dict); ok && start+n <= len(raw) {
		if bytes.HasPrefix(bytes.TrimLeft(raw[start+n:], "\r\n \t"), []byte("endstream")) {
			return start + n, start + n + bytes.Index(raw[start+n:], []byte("endstream")) + 9
		}
	}
	rel := bytes.Index(raw[start:], []byte("endstream"))
	if rel < 0 {
		return -1, 0
	}
	end := start + rel
	for end > start && (raw[end-1] == '\n' || raw[end-1] == '\r') {
		end--
	}
	return end, start + rel + 9
}

func pdfLength(dict []byte) (int, bool) {
	i := bytes.LastIndex(dict, []byte("/Length"))
	if i < 0 {
		return 0, false
	}
	s := strings.TrimLeft(string(dict[i+7:]), " \t\r\n")
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, false
	}
	rest := strings.TrimLeft(s[j:], " \t\r\n")
	if rest != "" && (rest[0] == 'R' || rest[0] >= '0' && rest[0] <= '9') {
		return 0, false
	}
	n, err := strconv.Atoi(s[:j])
	return n, err == nil
}

type pdfFilter struct {
	name string
	at   int
}

func decodeStream(data, dict []byte, total *int64) ([]byte, error) {
	out, err := applyFilters(data, dict)
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxPDFTotal-*total {
		return nil, fail(CodeTooLarge, "PDF streams exceed 64 MiB decoded", "")
	}
	*total += int64(len(out))
	return out, nil
}

func applyFilters(data []byte, dict []byte) ([]byte, error) {
	for _, f := range streamFilters(dict) {
		var err error
		switch f.name {
		case "flate":
			var z io.ReadCloser
			if z, err = zlib.NewReader(bytes.NewReader(data)); err == nil {
				data, err = readLimited(z)
				if cerr := z.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}
		case "a85":
			data, err = readLimited(ascii85.NewDecoder(bytes.NewReader(data)))
		case "lzw":
			z := lzw.NewReader(bytes.NewReader(data), lzw.MSB, 8)
			data, err = readLimited(z)
			if cerr := z.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
		if err != nil {
			return nil, errSkipStream
		}
	}
	return data, nil
}

func streamFilters(dict []byte) []pdfFilter {
	var fs []pdfFilter
	for _, f := range []struct{ needle, name string }{
		{"/ASCII85Decode", "a85"}, {"/LZWDecode", "lzw"}, {"/FlateDecode", "flate"},
	} {
		if at := bytes.Index(dict, []byte(f.needle)); at >= 0 {
			fs = append(fs, pdfFilter{f.name, at})
		}
	}
	for i := range fs {
		for j := i + 1; j < len(fs); j++ {
			if fs[j].at < fs[i].at {
				fs[i], fs[j] = fs[j], fs[i]
			}
		}
	}
	return fs
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPDFTotal+1))
	if err != nil {
		return nil, fmt.Errorf("media/doc: read PDF stream: %w", err)
	}
	return data, nil
}

func isCMap(data []byte) bool {
	return bytes.Contains(data, []byte("beginbfchar")) || bytes.Contains(data, []byte("beginbfrange"))
}

func looksContent(data []byte) bool {
	return bytes.Contains(data, []byte("BT")) || bytes.Contains(data, []byte("Tj")) || bytes.Contains(data, []byte("TJ"))
}

func parseCMap(data []byte, c *cmapTable) {
	s := string(data)
	for _, tag := range []string{"bfchar", "bfrange"} {
		rest := s
		for {
			i := strings.Index(rest, "begin"+tag)
			if i < 0 {
				break
			}
			j := strings.Index(rest[i:], "end"+tag)
			if j < 0 {
				break
			}
			cmapBlock(rest[i+len(tag)+5:i+j], tag == "bfrange", c)
			rest = rest[i+j+len(tag)+3:]
		}
	}
}

func cmapBlock(body string, isRange bool, c *cmapTable) {
	tok := hexTokens(body)
	for i := 0; i < len(tok); {
		if isRange {
			i = cmapRange(tok, i, c)
		} else {
			i = cmapChar(tok, i, c)
		}
	}
}

func cmapChar(tok []string, i int, c *cmapTable) int {
	lo, ok := hexNum(tok[i])
	if !ok {
		return i + 1
	}
	if i+1 < len(tok) {
		if dst, good := hexBytes(tok[i+1]); good {
			cAdd(c, lo, lo, dst)
		}
	}
	return i + 2
}

func cmapRange(tok []string, i int, c *cmapTable) int {
	lo, ok := hexNum(tok[i])
	if !ok {
		return i + 1
	}
	if i+2 >= len(tok) {
		return len(tok)
	}
	hi, ok := hexNum(tok[i+1])
	if !ok || hi < lo || hi-lo > maxCMapEntries {
		return i + 1
	}
	if tok[i+2] != "[" {
		if dst, good := hexBytes(tok[i+2]); good {
			cAdd(c, lo, hi, dst)
		}
		return i + 3
	}
	code := lo
	for i += 3; i < len(tok) && tok[i] != "]" && code <= hi; i++ {
		if dst, good := hexBytes(tok[i]); good {
			cAdd(c, code, code, dst)
		}
		code++
	}
	return i
}

func cAdd(c *cmapTable, lo, hi uint32, dst []byte) {
	if len(dst) > c.width {
		c.width = len(dst)
	}
	base := cmapString(dst)
	for code := lo; code <= hi && len(c.m) < maxCMapEntries; code++ {
		r := []rune(base)
		if len(r) > 0 {
			r[len(r)-1] += rune(code - lo) // #nosec G115 -- code-lo <= maxCMapEntries
		}
		c.m[code] = string(r)
	}
}

func hexTokens(body string) []string {
	var out []string
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '<':
			if i+1 < len(body) && body[i+1] == '<' {
				i++
				continue
			}
			j := strings.IndexByte(body[i+1:], '>')
			if j < 0 {
				return out
			}
			out = append(out, body[i+1:i+1+j])
			i += j + 1
		case '[', ']':
			out = append(out, string(body[i]))
		}
	}
	return out
}

func hexNum(s string) (uint32, bool) {
	b, ok := hexBytes(s)
	if !ok || len(b) == 0 || len(b) > 4 {
		return 0, false
	}
	var v uint32
	for _, x := range b {
		v = v<<8 | uint32(x)
	}
	return v, true
}

func hexBytes(s string) ([]byte, bool) {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, s)
	if len(s)%2 != 0 {
		s += "0"
	}
	b := make([]byte, len(s)/2)
	for i := range b {
		hi, lo := nibble(s[2*i]), nibble(s[2*i+1])
		if hi < 0 || lo < 0 {
			return nil, false
		}
		b[i] = byte(hi<<4 | lo) // #nosec G115 -- nibbles are 0..15
	}
	return b, true
}

func nibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

func cmapString(b []byte) string {
	if len(b) == 1 {
		return string(rune(b[0]))
	}
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u))
}

func pdfText(data []byte, c *cmapTable) string {
	var out strings.Builder
	for i := 0; i < len(data); {
		switch {
		case data[i] == '(':
			s, n := literal(data[i:])
			out.WriteString(decodeStr(s, c))
			out.WriteByte(' ')
			i += n
		case data[i] == '<' && i+1 < len(data) && data[i+1] == '<':
			i = skipDict(data, i)
		case data[i] == '<':
			s, n := hexLiteral(data[i:])
			out.WriteString(decodeStr(s, c))
			out.WriteByte(' ')
			i += n
		default:
			i++
		}
	}
	return out.String()
}

func literal(data []byte) ([]byte, int) {
	var out []byte
	depth := 1
	for i := 1; i < len(data); i++ {
		switch data[i] {
		case '\\':
			if i++; i < len(data) {
				switch data[i] {
				case 'n':
					out = append(out, '\n')
				case 'r':
					out = append(out, '\r')
				case 't':
					out = append(out, '\t')
				default:
					out = append(out, data[i])
				}
			}
		case '(':
			depth++
			out = append(out, '(')
		case ')':
			if depth--; depth == 0 {
				return out, i + 1
			}
			out = append(out, ')')
		default:
			out = append(out, data[i])
		}
	}
	return out, len(data)
}

func hexLiteral(data []byte) ([]byte, int) {
	j := bytes.IndexByte(data[1:], '>')
	if j < 0 {
		b, _ := hexBytes(string(data[1:]))
		return b, len(data)
	}
	b, _ := hexBytes(string(data[1 : 1+j]))
	return b, j + 2
}

func skipDict(data []byte, i int) int {
	depth := 0
	for ; i < len(data)-1; i++ {
		if data[i] == '<' && data[i+1] == '<' {
			depth++
			i++
		} else if data[i] == '>' && data[i+1] == '>' {
			if depth--; depth == 0 {
				return i + 2
			}
			i++
		}
	}
	return len(data)
}

func decodeStr(raw []byte, c *cmapTable) string {
	if bytes.HasPrefix(raw, []byte{0xFE, 0xFF}) {
		return utf16Str(raw[2:])
	}
	if c == nil || len(c.m) == 0 {
		r := make([]rune, len(raw))
		for i, x := range raw {
			r[i] = rune(x)
		}
		return string(r)
	}
	width := max(1, min(c.width, 2))
	var b strings.Builder
	for i := 0; i+width <= len(raw); i += width {
		var code uint32
		if width == 2 {
			code = uint32(raw[i])<<8 | uint32(raw[i+1])
		} else {
			code = uint32(raw[i])
		}
		if s, ok := c.m[code]; ok {
			b.WriteString(s)
		} else if width == 2 {
			b.WriteString(utf16Str([]byte{raw[i], raw[i+1]}))
		} else {
			b.WriteRune(rune(code)) // #nosec G115 -- width 1 code is <= 255
		}
	}
	return b.String()
}

func utf16Str(raw []byte) string {
	u := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		u = append(u, uint16(raw[i])<<8|uint16(raw[i+1]))
	}
	return string(utf16.Decode(u))
}
