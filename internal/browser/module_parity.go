//go:build !minimal

package browser

import (
	"context"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// console returns the buffered console log of the current page. The page text
// is page-controlled, so the result is bounded by Page.Console and tainted
// (SPEC §14.5, ADR-070); the core redacts the body before the model sees it.
func (m *Module) console(ctx context.Context) (host.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	text, err := p.Console(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	return host.Result{Content: text, UntrustedSource: m.taint()}, nil
}

// getImages lists the images of the current page as one bounded line each
// (TOOL-009).
func (m *Module) getImages(ctx context.Context) (host.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	images, err := p.Images(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	return host.Result{
		Content: formatImages(images), Meta: map[string]any{"images": len(images)},
		UntrustedSource: m.taint(),
	}, nil
}

// formatImages renders one line per image. Natural size is 0 for an image that
// has not decoded yet, which is reported as-is instead of guessed.
func formatImages(images []Image) string {
	if len(images) == 0 {
		return "no images on the page"
	}
	var out strings.Builder
	for i, img := range images {
		if i > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprint(&out, i+1)
		out.WriteString(". ")
		out.WriteString(img.Src)
		fmt.Fprintf(&out, " (%dx%d)", img.Width, img.Height)
		if img.Alt != "" {
			fmt.Fprintf(&out, " alt=%q", img.Alt)
		}
	}
	return out.String()
}
