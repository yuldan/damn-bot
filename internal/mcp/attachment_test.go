package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func TestMCPImageContentMapsToAttachment(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"content":[{"type":"text","text":"see image"},` +
		`{"type":"image","mimeType":"image/png","data":"` +
		base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfixture")) + `"}]}`)
	res, err := (&Client{server: Server{Name: "srv"}}).mapResult("render", json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "see image" || len(res.Attachments) != 1 {
		t.Fatalf("result = %+v", res)
	}
	a := res.Attachments[0]
	if a.Kind != host.AttachmentImage || a.MIME != "image/png" || !strings.HasPrefix(a.Name, "mcp:srv:render") {
		t.Fatalf("attachment = %+v", a)
	}
	if strings.Contains(res.Content, "base64") || res.Meta["content"] != nil {
		t.Fatalf("image leaked into text/meta: %+v", res)
	}
}

func TestMCPTextResultControl(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"content":[{"type":"text","text":"plain result"}]}`)
	res, err := (&Client{server: Server{Name: "srv"}}).mapResult("echo", raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "plain result" || len(res.Attachments) != 0 {
		t.Fatalf("control = %+v", res)
	}
}

func TestMCPImageOversizeRejected(t *testing.T) {
	t.Parallel()
	big := make([]byte, host.MaxAttachmentBytes(host.AttachmentImage)+1)
	raw, err := json.Marshal(map[string]any{"content": []map[string]string{{
		"type": "image", "mimeType": "image/png",
		"data": base64.StdEncoding.EncodeToString(big),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Client{server: Server{Name: "srv"}}).mapResult("big", raw)
	if err == nil || !strings.Contains(err.Error(), "too_large") {
		t.Fatalf("oversize = %v", err)
	}
}

func TestMCPImageBadBase64(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"content":[{"type":"image","mimeType":"image/png","data":"!!!"}]}`)
	_, err := (&Client{server: Server{Name: "srv"}}).mapResult("bad", raw)
	if err == nil || !strings.Contains(err.Error(), "invalid_args") {
		t.Fatalf("bad base64 = %v", err)
	}
}
