package main

import (
	"encoding/json"
	"html"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/acp"
	"github.com/damnbot/damnbot/internal/tools"
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
// up to the P5 inline page; larger or binary (`blob`) resources are refused
// rather than silently dropped, because the blob spill store is not wired to
// the ACP surface.
const (
	maxResourceChars = 4_000  // P5 inline page
	maxResourceTotal = 24_000 // P5 per-turn inline aggregate
	maxResourceURI   = 2_048
)

// promptBlock is one inbound session/prompt content block. Unknown fields are
// ignored (forward tolerance); unknown types are ignored too.
type promptBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	URI      string `json:"uri"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Resource *struct {
		URI      string  `json:"uri"`
		MimeType string  `json:"mimeType"`
		Text     *string `json:"text"`
		Blob     *string `json:"blob"`
	} `json:"resource"`
}

// promptText merges the inbound blocks into one prompt string. Resource text
// passes the redactor and the untrusted-content gate before it reaches the
// model (SPEC §14.3/§14.5); resource links are rendered as a URI-only line.
func (s *acpServer) promptText(blocks []promptBlock) (string, *contentFailure) {
	var b strings.Builder
	total := 0
	for i := range blocks {
		c := &blocks[i]
		switch c.Type {
		case "text":
			b.WriteString(c.Text)
		case "resource":
			text, uri, ferr := s.resourceText(c)
			if ferr != nil {
				return "", ferr
			}
			total += utf8.RuneCountInString(text)
			if total > maxResourceTotal {
				return "", contentErrorf("too_large")
			}
			b.WriteString("\n<untrusted source=\"acp:resource\" uri=\"")
			b.WriteString(html.EscapeString(uri))
			b.WriteString("\">")
			b.WriteString(tools.EscapeUntrustedClose(text))
			b.WriteString("</untrusted>")
		case "resource_link":
			uri, ferr := resourceURI(c.URI)
			if ferr != nil {
				return "", ferr
			}
			b.WriteString("\n<resource_link uri=\"")
			b.WriteString(html.EscapeString(uri))
			b.WriteString("\" name=\"")
			b.WriteString(html.EscapeString(c.Name))
			b.WriteString("\"/>")
		}
	}
	return b.String(), nil
}

// resourceText validates one embedded resource and returns its redacted text.
func (s *acpServer) resourceText(c *promptBlock) (text, uri string, ferr *contentFailure) {
	r := c.Resource
	if r == nil {
		return "", "", contentErrorf("invalid_resource")
	}
	uri, ferr = resourceURI(r.URI)
	if ferr != nil {
		return "", "", ferr
	}
	if (r.Text == nil) == (r.Blob == nil) {
		return "", "", contentErrorf("invalid_resource")
	}
	if r.Blob != nil {
		// A blob has no text projection yet: refuse instead of dropping it.
		return "", "", contentErrorf("unsupported_resource")
	}
	if utf8.RuneCountInString(*r.Text) > maxResourceChars {
		return "", "", contentErrorf("too_large")
	}
	text = *r.Text
	if s.deps.red != nil {
		text = s.deps.red.String(text)
	}
	return text, uri, nil
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
