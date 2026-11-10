package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestHelpOffline: the compiled-in docs are searchable, bounded by 250 KiB
// (which also bounds their brotli size), and carry no external URL; the HTML
// session export is self-contained (GAP-UX-08).
func TestHelpOffline(t *testing.T) {
	t.Parallel()
	docs := helpDocs()
	total := 0
	for _, d := range docs {
		total += len(d.Body)
		if strings.TrimSpace(d.Body) == "" {
			t.Errorf("%s is empty", d.Name)
		}
		if externalURL(d.Body) {
			t.Errorf("%s carries an external URL", d.Name)
		}
	}
	if total > maxHelpDocsBytes {
		t.Fatalf("docs = %d bytes > %d", total, maxHelpDocsBytes)
	}
	var stdout, stderr bytes.Buffer
	if code := helpDocsCommand([]string{"config"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "config") {
		t.Fatalf("search = %d %q %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := helpDocsCommand(nil, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "guide") {
		t.Fatalf("index = %d %q", code, stdout.String())
	}

	var html bytes.Buffer
	err := writeExport(&html, "html", &exportMeta{ID: "s1", Title: "session"}, []ctxdb.Event{
		{Seq: 1, Role: "user", Content: "hello <script>alert(1)</script>"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := html.String()
	if externalURL(out) || strings.Contains(out, "<script") || strings.Contains(out, "<link") {
		t.Fatalf("export is not self-contained: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("export did not escape content: %q", out)
	}
}
