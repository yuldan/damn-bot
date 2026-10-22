package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
)

// exportFormatOK reports the SPEC §10 `sessions export --format` values.
func exportFormatOK(format string) bool {
	switch format {
	case "md", "jsonl", "html":
		return true
	}
	return false
}

// exportMeta is the header of a rendered transcript.
type exportMeta struct {
	ID, Title, Model, Origin, Cwd string
	CreatedAt                     int64
}

// writeExport renders one transcript. Content and payload are redacted
// before rendering unless red is nil; every renderer escapes what its format
// requires (HTML entity, JSON encoding), so exported bytes never execute or
// break the document structure.
func writeExport(w io.Writer, format string, meta *exportMeta, events []ctxdb.Event, red *redact.Redactor) error {
	if !exportFormatOK(format) {
		return fmt.Errorf("unknown format %q (want md, jsonl, or html)", format)
	}
	clean := func(s string) string {
		if red != nil {
			return red.String(s)
		}
		return s
	}
	switch format {
	case "jsonl":
		return writeJSONL(w, events, clean)
	case "html":
		return writeHTML(w, meta, events, clean)
	default:
		return writeMarkdown(w, meta, events, clean)
	}
}

func writeJSONL(w io.Writer, events []ctxdb.Event, clean func(string) string) error {
	enc := json.NewEncoder(w)
	for i := range events {
		e := &events[i]
		row := struct {
			Session   string `json:"session"`
			Seq       int64  `json:"seq"`
			Kind      string `json:"kind"`
			Role      string `json:"role,omitempty"`
			Tool      string `json:"tool,omitempty"`
			CreatedAt int64  `json:"created_at"`
			Content   string `json:"content,omitempty"`
			Payload   string `json:"payload,omitempty"`
		}{e.SessionID, e.Seq, e.Kind, e.Role, e.ToolName, e.CreatedAt, clean(e.Content), clean(e.Payload)}
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("export jsonl: %w", err)
		}
	}
	return nil
}

func writeMarkdown(w io.Writer, meta *exportMeta, events []ctxdb.Event, clean func(string) string) error {
	title := meta.Title
	if title == "" {
		title = meta.ID
	}
	if _, err := fmt.Fprintf(w, "# %s\n\n- session: `%s`\n- model: `%s`\n- origin: `%s`\n- created: %s\n\n",
		title, meta.ID, meta.Model, meta.Origin, formatTime(meta.CreatedAt)); err != nil {
		return fmt.Errorf("export markdown: %w", err)
	}
	for i := range events {
		e := &events[i]
		role := e.Role
		if role == "" {
			role = e.Kind
		}
		if _, err := fmt.Fprintf(w, "### %s (%d)\n\n", role, e.Seq); err != nil {
			return fmt.Errorf("export markdown: %w", err)
		}
		if c := clean(e.Content); c != "" {
			if _, err := fmt.Fprintf(w, "%s\n\n", c); err != nil {
				return fmt.Errorf("export markdown: %w", err)
			}
		}
		if p := clean(e.Payload); p != "" {
			if _, err := fmt.Fprintf(w, "```json\n%s\n```\n\n", p); err != nil {
				return fmt.Errorf("export markdown: %w", err)
			}
		}
	}
	return nil
}

func writeHTML(w io.Writer, meta *exportMeta, events []ctxdb.Event, clean func(string) string) error {
	title := meta.Title
	if title == "" {
		title = meta.ID
	}
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title></head><body>\n")
	fmt.Fprintf(&b, "<h1>%s</h1>\n<p>session %s · model %s · %s</p>\n",
		html.EscapeString(title), html.EscapeString(meta.ID), html.EscapeString(meta.Model), formatTime(meta.CreatedAt))
	for i := range events {
		e := &events[i]
		role := e.Role
		if role == "" {
			role = e.Kind
		}
		fmt.Fprintf(&b, "<article><h2>%s (%d)</h2>\n", html.EscapeString(role), e.Seq)
		if c := clean(e.Content); c != "" {
			fmt.Fprintf(&b, "<pre>%s</pre>\n", html.EscapeString(c))
		}
		if p := clean(e.Payload); p != "" {
			fmt.Fprintf(&b, "<pre class=\"payload\">%s</pre>\n", html.EscapeString(p))
		}
		b.WriteString("</article>\n")
	}
	b.WriteString("</body></html>\n")
	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("export html: %w", err)
	}
	return nil
}

// formatTime renders a store timestamp; 0 is the zero time of fixture rows.
func formatTime(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
