package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/acp"
	"github.com/damnbot/damnbot/internal/tools"
	"github.com/damnbot/damnbot/internal/tui"
)

// contentFailure is a malformed or oversized inbound content block. The
// protocol text is fixed; params never reach it (SPEC §13.3).
type contentFailure struct {
	code string
}

func contentErrorf(code string) *contentFailure { return &contentFailure{code: code} }

// protocolError renders f as -32602 (invalid params) carrying the SPEC §4.3
// error code in data.error_code.
func (f *contentFailure) protocolError() *acp.Error {
	msg := "invalid resource block"
	switch f.code {
	case "unsupported_resource":
		msg = "unsupported resource encoding"
	case "too_large":
		msg = "resource exceeds the inline limit"
	case "internal":
		msg = "internal error"
	}
	data, err := json.Marshal(map[string]string{"error_code": f.code})
	if err != nil {
		data = []byte(`{"error_code":"internal"}`)
	}
	code := acp.CodeInvalidParams
	if f.code == "internal" {
		code = acp.CodeInternal
	}
	return &acp.Error{Code: code, Message: msg, Data: data}
}

// Inbound content blocks (SPEC §13.3, GAP-UX-04). Text resources are inlined
// up to the P5 page; larger text and binary (`blob`) resources are spilled to
// the ctxdb blob store and referenced by blob_sha.
const (
	maxResourceChars     = 4_000  // P5 inline page
	maxResourceTotal     = 24_000 // P5 per-turn inline aggregate
	maxResourceURI       = 2_048
	maxResourceBlobBytes = 8 << 20
)

type resourceSpiller func(ctx context.Context, name, mime string, data []byte) (tui.Attachment, error)

// promptBlock is one inbound session/prompt content block. Unknown fields are
// ignored (forward tolerance); `_meta` is retained raw for valid resources.
type promptBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	URI      string          `json:"uri"`
	Name     string          `json:"name"`
	MimeType string          `json:"mimeType"`
	Meta     json.RawMessage `json:"_meta"`
	Resource *struct {
		URI      string          `json:"uri"`
		MimeType string          `json:"mimeType"`
		Text     *string         `json:"text"`
		Blob     *string         `json:"blob"`
		Meta     json.RawMessage `json:"_meta"`
	} `json:"resource"`
}

// promptText merges inbound blocks into one prompt string and the attachments
// that belong to the user message. Resource text passes the redactor before it
// is inlined or spilled; `_meta` is returned raw.
func (s *acpServer) promptText(ctx context.Context, blocks []promptBlock, spill resourceSpiller) (string, []tui.Attachment, json.RawMessage, *contentFailure) {
	var b strings.Builder
	var atts []tui.Attachment
	var metas []json.RawMessage
	total := 0
	for i := range blocks {
		c := &blocks[i]
		switch c.Type {
		case "text":
			b.WriteString(c.Text)
		case "resource":
			if ferr := s.appendResource(ctx, c, spill, &b, &atts, &metas, &total); ferr != nil {
				return "", nil, nil, ferr
			}
		case "resource_link":
			if ferr := appendResourceLink(&b, c); ferr != nil {
				return "", nil, nil, ferr
			}
		}
	}
	meta, ferr := marshalResourceMeta(metas)
	if ferr != nil {
		return "", nil, nil, ferr
	}
	return b.String(), atts, meta, nil
}

func (s *acpServer) appendResource(ctx context.Context, c *promptBlock, spill resourceSpiller, b *strings.Builder, atts *[]tui.Attachment, metas *[]json.RawMessage, total *int) *contentFailure {
	r := c.Resource
	if r == nil {
		return contentErrorf("invalid_resource")
	}
	uri, ferr := resourceURI(r.URI)
	if ferr != nil {
		return ferr
	}
	if (r.Text == nil) == (r.Blob == nil) {
		return contentErrorf("invalid_resource")
	}
	if meta := resourceMeta(c); len(meta) > 0 {
		*metas = append(*metas, meta)
	}
	if r.Blob != nil {
		data, derr := decodeResourceBlob(*r.Blob)
		if derr != nil {
			return derr
		}
		return appendSpilled(ctx, spill, b, atts, uri, cmpMIME(r.MimeType, "application/octet-stream"), data)
	}
	text := *r.Text
	if s.deps.red != nil {
		text = s.deps.red.String(text)
	}
	n := utf8.RuneCountInString(text)
	if n > maxResourceChars || *total+n > maxResourceTotal {
		return appendSpilled(ctx, spill, b, atts, uri, cmpMIME(r.MimeType, "text/plain"), []byte(text))
	}
	*total += n
	b.WriteString("\n<untrusted source=\"acp:resource\" uri=\"")
	b.WriteString(html.EscapeString(uri))
	b.WriteString("\">")
	b.WriteString(tools.EscapeUntrustedClose(text))
	b.WriteString("</untrusted>")
	return nil
}

func appendSpilled(ctx context.Context, spill resourceSpiller, b *strings.Builder, atts *[]tui.Attachment, uri, mime string, data []byte) *contentFailure {
	att, ferr := spillResource(ctx, spill, uri, mime, data)
	if ferr != nil {
		return ferr
	}
	*atts = append(*atts, att)
	writeResourceRef(b, uri, att)
	return nil
}

func appendResourceLink(b *strings.Builder, c *promptBlock) *contentFailure {
	uri, ferr := resourceURI(c.URI)
	if ferr != nil {
		return ferr
	}
	b.WriteString("\n<resource_link uri=\"")
	b.WriteString(html.EscapeString(uri))
	b.WriteString("\" name=\"")
	b.WriteString(html.EscapeString(c.Name))
	b.WriteString("\"/>")
	return nil
}

func marshalResourceMeta(metas []json.RawMessage) (json.RawMessage, *contentFailure) {
	if len(metas) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(metas)
	if err != nil {
		return nil, contentErrorf("internal")
	}
	return raw, nil
}

func resourceMeta(c *promptBlock) json.RawMessage {
	if len(c.Meta) > 0 {
		return c.Meta
	}
	if c.Resource != nil {
		return c.Resource.Meta
	}
	return nil
}

func cmpMIME(got, def string) string {
	if got == "" {
		return def
	}
	return got
}

func decodeResourceBlob(encoded string) ([]byte, *contentFailure) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maxResourceBlobBytes) {
		return nil, contentErrorf("too_large")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, contentErrorf("unsupported_resource")
	}
	if len(data) > maxResourceBlobBytes {
		return nil, contentErrorf("too_large")
	}
	return data, nil
}

func spillResource(ctx context.Context, spill resourceSpiller, name, mime string, data []byte) (tui.Attachment, *contentFailure) {
	if spill == nil {
		return tui.Attachment{}, contentErrorf("unsupported_resource")
	}
	att, err := spill(ctx, name, mime, data)
	if err != nil || att.BlobSHA == "" {
		return tui.Attachment{}, contentErrorf("unsupported_resource")
	}
	return att, nil
}

func writeResourceRef(b *strings.Builder, uri string, att tui.Attachment) {
	fmt.Fprintf(b, "\n<resource uri=\"%s\" blob_sha=\"%s\" size=\"%d\" mime=\"%s\"/>", html.EscapeString(uri), att.BlobSHA, att.Size, html.EscapeString(att.MIME))
}

// resourceURI validates an absolute, bounded URI without control characters.
func resourceURI(raw string) (string, *contentFailure) {
	if raw == "" || len(raw) > maxResourceURI {
		return "", contentErrorf("invalid_resource")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", contentErrorf("invalid_resource")
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "", contentErrorf("invalid_resource")
	}
	return u.String(), nil
}

// decodePromptBlocks decodes the prompt array leniently: a block with an
// unknown shape still decodes (its fields stay zero) and is ignored.
func decodePromptBlocks(raw []json.RawMessage) ([]promptBlock, *contentFailure) {
	out := make([]promptBlock, 0, len(raw))
	for _, r := range raw {
		var c promptBlock
		if err := json.Unmarshal(r, &c); err != nil {
			return nil, contentErrorf("invalid_resource")
		}
		out = append(out, c)
	}
	return out, nil
}
