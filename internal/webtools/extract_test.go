//go:build !minimal

package webtools

import (
	"errors"
	"strings"
	"testing"
)

const sampleHTML = `<html><head><title>Judul</title><style>.x{color:red}</style></head><body>
<h1>Heading</h1>
<p>Hello <strong>bold</strong> and <a href="https://example.test/x">link</a>.</p>
<script>alert("bad")</script>
<pre><code>a &lt; b</code></pre>
<table><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>
<ul><li>one</li><li>two</li></ul>
</body></html>`

func TestExtractMarkdown(t *testing.T) {
	t.Parallel()
	doc, err := newExtractor(0).Extract("text/html; charset=utf-8", []byte(sampleHTML))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, want := range []string{
		"Heading", "Hello bold and [link](https://example.test/x)",
		"```", "a < b", "| A | B |", "|---|---|", "| 1 | 2 |", "one", "two",
	} {
		if !strings.Contains(doc.Markdown, want) {
			t.Errorf("markdown missing %q:\n%s", want, doc.Markdown)
		}
	}
	for _, bad := range []string{"alert", "color:red", "<h1>", "<table>"} {
		if strings.Contains(doc.Markdown, bad) {
			t.Errorf("markdown leaks %q:\n%s", bad, doc.Markdown)
		}
	}
	if doc.Charset != "utf-8" || doc.Truncated {
		t.Errorf("doc meta = %+v", doc)
	}
}

func TestExtractDeterministic(t *testing.T) {
	t.Parallel()
	e := newExtractor(0)
	a, err := e.Extract("text/html", []byte(sampleHTML))
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Extract("text/html", []byte(sampleHTML))
	if err != nil || a.Markdown != b.Markdown {
		t.Fatalf("second extract differs: %v", err)
	}
}

func TestExtractCharset(t *testing.T) {
	t.Parallel()
	doc, err := newExtractor(0).Extract("text/html; charset=iso-8859-1", []byte("<p>caf\xe9</p>"))
	if err != nil || doc.Charset != "iso-8859-1" || !strings.Contains(doc.Markdown, "café") {
		t.Fatalf("doc = %+v, err = %v", doc, err)
	}
	doc, err = newExtractor(0).Extract("text/html", []byte("<meta charset=\"utf-8\"><p>naïve</p>"))
	if err != nil || doc.Charset != "utf-8" || !strings.Contains(doc.Markdown, "naïve") {
		t.Fatalf("meta doc = %+v, err = %v", doc, err)
	}
}

func TestExtractBinaryAndContentType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, ct string
		body     []byte
	}{
		{"nul", "text/html", []byte("abc\x00def")},
		{"image", "image/png", []byte("not an image")},
		{"pdf", "application/pdf", []byte("%PDF-1.7")},
		{"bad_utf8", "text/html; charset=utf-8", []byte{0xff, 0xfe, 0xfd}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := newExtractor(0).Extract(tc.ct, tc.body); !errors.Is(err, errBinary) {
				t.Errorf("err = %v, want errBinary", err)
			}
		})
	}
}

func TestExtractTruncates(t *testing.T) {
	t.Parallel()
	body := "<p>" + strings.Repeat("kata ", 100) + "</p>"
	doc, err := newExtractor(40).Extract("text/html", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Truncated || !strings.HasSuffix(doc.Markdown, "[truncated]") || len(doc.Markdown) > 80 {
		t.Fatalf("truncated doc len=%d suffix=%q", len(doc.Markdown), doc.Markdown[len(doc.Markdown)-12:])
	}
}

func TestExtractEscapesAndLinks(t *testing.T) {
	t.Parallel()
	doc, err := newExtractor(0).Extract("text/html", []byte(
		`<p>&lt;tag&gt; &amp; <a href="javascript:alert(1)">x</a> <a href="//evil.test/">y</a></p>`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Markdown, "<tag> &") {
		t.Errorf("entities not decoded: %q", doc.Markdown)
	}
	for _, bad := range []string{"javascript:", "](//evil.test"} {
		if strings.Contains(doc.Markdown, bad) {
			t.Errorf("unsafe link leaked %q: %q", bad, doc.Markdown)
		}
	}
}
