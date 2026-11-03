package main

import (
	"bytes"
	stdimage "image"
	"image/png"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

func attachmentPNG(t *testing.T) []byte {
	t.Helper()
	img := stdimage.NewNRGBA(stdimage.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func attachmentAdapter(t *testing.T, url string, vision bool) *providerAdapter {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	client, err := provider.NewOpenAI(n.Client("inference", netx.Inference), &provider.Config{
		Routes: []provider.Route{{Provider: "fake", Model: "v", BaseURL: url, APIKey: "k", Vision: vision, ContextWindow: 128_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &providerAdapter{client: client, model: "fake/v"}
}

func TestAttachmentReachesVisionProvider(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("ok"))
	req := &loop.Request{Model: "fake/v", MaxTokens: 64, Messages: []loop.Message{
		{Role: "user", Content: "describe"},
		{Role: "tool", ToolCallID: "c1", Content: "see attachment", Attachments: []loop.Attachment{{
			Kind: "image", Name: "cat.png", MIME: "image/png", Data: attachmentPNG(t),
		}}},
	}}
	if _, err := attachmentAdapter(t, srv.URL, true).Stream(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Requests()[0])
	if !strings.Contains(body, `"image_url"`) || !strings.Contains(body, "data:image/png;base64,") {
		t.Fatalf("vision body = %s", body)
	}
}

func TestAttachmentTextOnlyRejected(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("wrong"))
	req := &loop.Request{Model: "fake/v", MaxTokens: 64, Messages: []loop.Message{
		{Role: "user", Content: "describe"},
		{Role: "tool", ToolCallID: "c1", Content: "see", Attachments: []loop.Attachment{{
			Kind: "image", Name: "cat.png", MIME: "image/png", Data: attachmentPNG(t),
		}}},
	}}
	if _, err := attachmentAdapter(t, srv.URL, false).Stream(t.Context(), req, nil); err == nil {
		t.Fatal("text-only route accepted an image")
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("text-only route got %d requests", len(srv.Requests()))
	}
}

func TestAttachmentTextControl(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("ok"))
	req := &loop.Request{Model: "fake/v", MaxTokens: 64, Messages: []loop.Message{
		{Role: "tool", ToolCallID: "c1", Content: "plain result"},
	}}
	if _, err := attachmentAdapter(t, srv.URL, true).Stream(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if body := string(srv.Requests()[0]); strings.Contains(body, `"image_url"`) {
		t.Fatalf("text result gained an image: %s", body)
	}
}

func TestAttachHistory(t *testing.T) {
	t.Parallel()
	a := loop.Attachment{Kind: "image", Name: "x", MIME: "image/png", BlobSHA: "abc"}
	history := []loop.Message{{Role: "tool", ToolCallID: "c1", Attachments: []loop.Attachment{a}}}
	out := []loop.Message{{Role: "user"}, {Role: "tool", ToolCallID: "c1"}}
	attachHistory(history, out)
	if len(out[1].Attachments) != 1 || out[1].Attachments[0].BlobSHA != "abc" {
		t.Fatalf("attachments = %+v", out[1].Attachments)
	}
	if len(out[0].Attachments) != 0 {
		t.Fatal("user message gained an attachment")
	}
}
