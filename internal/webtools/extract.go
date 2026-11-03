//go:build !minimal

package webtools

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"regexp"
	"strings"
	"unicode/utf8"
)

type document struct {
	Markdown, Charset string
	Truncated         bool
}

type extractor struct {
	MaxOutput int
	re        []*regexp.Regexp
}

func newExtractor(maxOutput int) *extractor {
	if maxOutput <= 0 {
		maxOutput = 128 << 10
	}
	patterns := []string{
		`(?is)<script\b[^>]*>.*?</script\s*>|<style\b[^>]*>.*?</style\s*>`, `(?s)<!--.*?-->`,
		`(?is)<pre[^>]*>(.*?)</pre\s*>`, `(?is)<table\b.*?</table\s*>`,
		`(?is)<tr\b.*?</tr\s*>`, `(?is)<t[hd]\b[^>]*>(.*?)</t[hd]\s*>`,
		`(?is)<a\b[^>]*\bhref\s*=\s*["']([^"']*)["'][^>]*>(.*?)</a\s*>`, `(?is)<code\b[^>]*>(.*?)</code\s*>`,
		`(?i)</?(?:p|div|section|article|header|footer|main|nav|aside|form|figure|blockquote|ul|ol|li|table|tr|br)\b[^>]*>`, `(?s)<[^>]*>`,
		`(?is)<h([1-6])\b[^>]*>(.*?)</h[1-6]\s*>`, `(?i)<li\b[^>]*>`,
	}
	e := &extractor{MaxOutput: maxOutput, re: make([]*regexp.Regexp, len(patterns))}
	for i, p := range patterns {
		e.re[i] = regexp.MustCompile(p)
	}
	return e
}

func (e *extractor) Extract(contentType string, body []byte) (document, error) {
	if looksBinary(body) {
		return document{}, errBinary
	}
	charset, text, err := decodeBody(contentType, body)
	if err != nil {
		return document{}, err
	}
	kind := strings.ToLower(contentType)
	if kind != "" && !strings.Contains(kind, "html") && !strings.HasPrefix(kind, "text/plain") {
		return document{}, fmt.Errorf("%w: content type %q", errBinary, kind)
	}
	doc := document{Charset: charset}
	if strings.HasPrefix(kind, "text/plain") {
		doc.Markdown = strings.TrimSpace(text)
	} else {
		doc.Markdown = e.markdown(text)
	}
	if len(doc.Markdown) > e.MaxOutput {
		cut := e.MaxOutput
		if i := strings.LastIndexByte(doc.Markdown[:cut], '\n'); i > 0 {
			cut = i
		}
		doc.Markdown = strings.TrimRight(doc.Markdown[:cut], " \t\n") + "\n\n[truncated]"
		doc.Truncated = true
	}
	return doc, nil
}

func (e *extractor) markdown(src string) string {
	md := e.re[0].ReplaceAllString(src, " ")
	md = e.re[1].ReplaceAllString(md, "")
	md = e.re[2].ReplaceAllStringFunc(md, func(s string) string {
		return "\n```\n" + strings.TrimSpace(e.re[2].FindStringSubmatch(s)[1]) + "\n```\n"
	})
	md = e.re[3].ReplaceAllStringFunc(md, e.table)
	md = e.re[6].ReplaceAllStringFunc(md, func(s string) string {
		m := e.re[6].FindStringSubmatch(s)
		if link := safeLink(m[1]); link != "" {
			return "[" + m[2] + "](" + link + ")"
		}
		return m[2]
	})
	md = e.re[7].ReplaceAllStringFunc(md, func(s string) string {
		return "`" + e.re[7].FindStringSubmatch(s)[1] + "`"
	})
	md = e.re[10].ReplaceAllStringFunc(md, func(s string) string {
		m := e.re[10].FindStringSubmatch(s)
		return "\n" + strings.Repeat("#", int(m[1][0]-'0')) + " " + m[2] + "\n"
	})
	md = e.re[11].ReplaceAllString(md, "\n- ")
	md = e.re[8].ReplaceAllString(md, "\n")
	md = e.re[9].ReplaceAllString(md, "")
	return cleanup(stdhtml.UnescapeString(md))
}

func (e *extractor) table(table string) string {
	var b strings.Builder
	for i, row := range e.re[4].FindAllString(table, -1) {
		cells := e.re[5].FindAllStringSubmatch(row, -1)
		if len(cells) == 0 {
			continue
		}
		b.WriteString("\n|")
		for _, cell := range cells {
			b.WriteString(" " + strings.ReplaceAll(strings.TrimSpace(cell[1]), "|", `\|`) + " |")
		}
		b.WriteString("\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat("---|", len(cells)) + "\n")
		}
	}
	return b.String()
}

func looksBinary(body []byte) bool {
	sample := body
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	ctrl := 0
	for _, c := range sample {
		if c < 0x09 || c > 0x0d && c < 0x20 {
			ctrl++
		}
	}
	return ctrl*20 > len(sample)
}

func decodeBody(contentType string, body []byte) (string, string, error) {
	body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
	declared := strings.ToLower(charsetFrom(contentType))
	switch declared {
	case "iso-8859-1", "latin1", "windows-1252":
		return declared, latin1(body), nil
	}
	if utf8.Valid(body) {
		if declared == "" {
			declared = "utf-8"
		}
		return declared, string(body), nil
	}
	if declared != "" {
		return "", "", fmt.Errorf("%w: invalid %s bytes", errBinary, declared)
	}
	return "iso-8859-1", latin1(body), nil
}

func charsetFrom(contentType string) string {
	lower := strings.ToLower(contentType)
	_, v, ok := strings.Cut(lower, "charset=")
	if !ok {
		return ""
	}
	v = strings.TrimSpace(v)
	if j := strings.IndexAny(v, " ;,"); j >= 0 {
		v = v[:j]
	}
	return strings.Trim(v, `"'`)
}

func latin1(body []byte) string {
	var b strings.Builder
	b.Grow(len(body))
	for _, c := range body {
		b.WriteRune(rune(c))
	}
	return b.String()
}

func safeLink(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "mailto:") || strings.HasPrefix(lower, "#") {
		return strings.TrimSpace(raw)
	}
	return ""
}

func cleanup(md string) string {
	lines := strings.Split(md, "\n")
	out := lines[:0]
	for _, line := range lines {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
