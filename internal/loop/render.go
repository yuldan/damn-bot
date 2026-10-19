package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// errorEnvelope is the model-facing tool failure shape of SPEC §4.3
// (E-68 golden).
func errorEnvelope(e *ToolError) string {
	b, err := json.Marshal(struct {
		OK    bool       `json:"ok"`
		Error *ToolError `json:"error"`
	}{OK: false, Error: e})
	if err != nil {
		return `{"ok":false,"error":{"code":"internal","message":"encode error"}}`
	}
	return string(b)
}

// runeLen counts characters, not bytes (P5 thresholds are characters).
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// preview returns at most n characters of s, cut on a rune boundary.
func preview(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// modelContent renders the model-facing tool content: an error envelope, the
// unchanged success text (EC-TCALL-007), or a P5 spill notice.
func (t *turn) modelContent(ctx context.Context, res *ToolResult) string {
	if res.Err != nil {
		return errorEnvelope(res.Err)
	}
	content := res.Content
	n := runeLen(content)
	if n > t.cfg.MaxInline {
		return t.trimmed(ctx, content, n, 800, true)
	}
	if t.cfg.MaxInlineTurn > 0 && t.inline+n > t.cfg.MaxInlineTurn {
		return t.trimmed(ctx, content, 0, 400, false)
	}
	t.inline += n
	return content
}

// trimmed spills or truncates an oversized result. keep is the preview
// length; tooLarge selects the honest too_large envelope when no Spiller is
// available for a single oversized result.
func (t *turn) trimmed(ctx context.Context, content string, n, keep int, tooLarge bool) string {
	if ref, err := t.spill(ctx, content); err == nil && ref != "" {
		out := preview(content, keep) + "\n[spilled: " + ref + "]"
		t.inline += runeLen(out)
		return out
	}
	if tooLarge {
		return errorEnvelope(&ToolError{
			Code:    CodeTooLarge,
			Message: fmt.Sprintf("tool output too large: %d chars", n),
			Hint:    "request a smaller range or page",
		})
	}
	return preview(content, keep) + "\n[truncated: turn inline budget]"
}

// spill asks the Spiller for a reference; a nil Spiller is an error.
func (t *turn) spill(ctx context.Context, content string) (string, error) {
	if t.deps.Spiller == nil {
		return "", errors.New("loop: no spiller")
	}
	ref, err := t.deps.Spiller.Spill(ctx, content)
	if err != nil {
		return "", fmt.Errorf("loop: spill: %w", err)
	}
	return ref, nil
}
