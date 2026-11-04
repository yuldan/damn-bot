//go:build !minimal

package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// TestF6GAPREL14MCPAttachment pins the F5 residual on the MCP side: an
// `image` content part becomes a validated host attachment blob, never
// base64 text or Meta, so the core can route it through the vision gate
// (`TestEdgeCases/EC-PROV-024` proves a text-only route makes 0 requests).
func TestF6GAPREL14MCPAttachment(t *testing.T) {
	t.Parallel()
	raw := []byte("\x89PNG\r\n\x1a\nmcp-fixture")
	payload, err := json.Marshal(map[string]any{"content": []map[string]string{
		{"type": "text", "text": "rendered"},
		{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(raw)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&Client{server: Server{Name: "srv"}}).mapResult("render", payload)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "rendered" || len(res.Attachments) != 1 {
		t.Fatalf("result = %+v", res)
	}
	att := res.Attachments[0]
	if att.Kind != host.AttachmentImage || att.MIME != "image/png" || !bytes.Equal(att.Data, raw) {
		t.Fatalf("attachment = %+v", att)
	}
	if strings.Contains(res.Content, "base64") || strings.Contains(res.Content, base64.StdEncoding.EncodeToString(raw)) {
		t.Fatalf("base64 leaked into content: %q", res.Content)
	}
	if res.Meta["content"] != nil {
		t.Fatalf("image leaked into meta: %+v", res.Meta)
	}
	if err := res.Validate(); err != nil {
		t.Fatalf("result does not validate: %v", err)
	}
}
