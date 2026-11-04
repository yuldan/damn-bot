//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// visionAttachmentName is the stable name of the screenshot attachment; the
// pipeline spills it as a blob and the vision route analyses it in the same
// turn (ADR-322), so no base64 ever enters Content.
const visionAttachmentName = "browser-vision.png"

// vision captures the viewport and returns the PNG as an image attachment
// (TOOL-016). The size preflight is the page's MaxScreenshot cap; the question
// is clipped into the result so the model sees the intent next to the image.
func (m *Module) vision(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a struct {
		Question string `json:"question"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	png, err := p.Screenshot(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	content := fmt.Sprintf("captured a %d byte PNG of the current page for vision analysis", len(png))
	if question := clip(a.Question, 1000); question != "" {
		content += "; question: " + question
	}
	return host.Result{
		Content:         content,
		Meta:            map[string]any{"bytes": len(png), "mime": "image/png"},
		UntrustedSource: m.taint(),
		Attachments: []host.Attachment{{
			Kind: host.AttachmentImage, Name: visionAttachmentName, MIME: "image/png", Data: png,
		}},
	}, nil
}
