package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// statusGlyph maps the normalized tool status to the SPEC §13.1 glyph.
func statusGlyph(s ToolStatus) string {
	switch s {
	case ToolOK:
		return "✓"
	case ToolFailed:
		return "✗"
	case ToolPending:
		return "⏸"
	case ToolRunning:
		return "…"
	default:
		return "?"
	}
}

// statusHint maps an explicit stream status to a normalized tool status; ""
// means the caller has no opinion and the running state is kept.
func statusHint(raw string) ToolStatus {
	switch raw {
	case "awaiting_approval", "pending":
		return ToolPending
	case "still_running", "running":
		return ToolRunning
	case "denied", "error", "failed", "timeout", "idle_timeout", "signaled", "spawn_failed", "cancelled_confirmed":
		return ToolFailed
	case "ok", "exited", "success", "completed":
		return ToolOK
	default:
		return ""
	}
}

// normalizeStatus derives the terminal status from the explicit stream status
// and the result text. The dispatcher drops ExecStatus today, so a shell
// header's exit code and the loop's error envelope are authoritative.
func normalizeStatus(raw, text string) ToolStatus {
	if s := statusHint(raw); s == ToolPending || s == ToolRunning {
		return s
	}
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, `{"ok":false`) {
		return ToolFailed
	}
	if code, ok := exitCodeValue(body); ok && code != 0 {
		return ToolFailed
	}
	if strings.HasPrefix(body, "[exit none") && !strings.Contains(clipRunes(body, 96), "signal none") {
		return ToolFailed
	}
	if s := statusHint(raw); s != "" {
		return s
	}
	return ToolOK
}

// renderToolBlock renders one tool block as plain text carrying every R1
// attribute (name, brief args, tag, status, duration, size, spill, undo).
func renderToolBlock(b *Block) string {
	var s strings.Builder
	s.WriteString("tool ")
	if b.ToolName == "" {
		s.WriteString("?")
	} else {
		s.WriteString(b.ToolName)
	}
	if brief := briefArgs(b.Args); brief != "" {
		s.WriteString(" " + brief)
	}
	if b.Tag != "" {
		s.WriteString(" #" + b.Tag)
	}
	s.WriteString(" " + statusGlyph(b.Status))
	if b.Duration > 0 {
		s.WriteString(" " + formatDuration(b.Duration))
	}
	if b.Size > 0 {
		s.WriteString(" " + formatSize(b.Size))
	}
	if b.ExitCode != nil && b.Status == ToolFailed {
		fmt.Fprintf(&s, " exit %d", *b.ExitCode)
	}
	if b.ActionID != 0 {
		fmt.Fprintf(&s, " ↶ %d", b.ActionID)
	}
	if b.Spill != "" {
		s.WriteString("\n  spill: " + b.Spill)
		if b.SpillSize > 0 {
			s.WriteString(" (" + formatSize(b.SpillSize) + " chars)")
		}
	}
	if preview := resultPreview(b.Text); preview != "" {
		s.WriteString("\n  " + preview)
	}
	return s.String()
}

// briefArgs picks the one argument that identifies a call. Raw arguments stay
// in Block.Args; only this renderer shortens them.
func briefArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) == nil {
		for _, k := range []string{"path", "command", "query", "pattern", "name"} {
			if v, ok := obj[k].(string); ok && v != "" {
				return clipRunes(strings.Join(strings.Fields(v), " "), 48)
			}
		}
	}
	return clipRunes(strings.Join(strings.Fields(raw), " "), 48)
}

// resultPreview renders the first meaningful line of a tool result; error
// envelopes become "code: message" instead of raw JSON.
func resultPreview(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if strings.HasPrefix(text, "{") && json.Unmarshal([]byte(text), &env) == nil && !env.OK && env.Error.Code != "" {
		return clipRunes(env.Error.Code+": "+env.Error.Message, 120)
	}
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return clipRunes(line, 120)
		}
	}
	return ""
}

// parseTag returns the CODE-TOOLS content tag (#hhhhhhhh) in a result.
func parseTag(s string) string {
	for i := 0; i+9 <= len(s); i++ {
		if s[i] == '#' && isHex8(s[i+1:i+9]) {
			return s[i+1 : i+9]
		}
	}
	return ""
}

func isHex8(s string) bool {
	for i := range len(s) {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

// parseSpill returns the blob path and full character count of a spilled
// result. The three markers are the shell, loop, and dispatcher forms.
func parseSpill(s string) (path string, size int) {
	for _, marker := range []string{"[full output: ", "[spilled: ", "[spill: "} {
		_, after, ok := strings.Cut(s, marker)
		if !ok {
			continue
		}
		body, _, ok := strings.Cut(after, "]")
		if !ok {
			continue
		}
		if j := strings.LastIndex(body, " ("); j > 0 && strings.HasSuffix(body, " chars)") {
			if n, err := strconv.Atoi(strings.TrimSuffix(body[j+2:], " chars)")); err == nil {
				return body[:j], n
			}
		}
		return body, 0
	}
	return "", 0
}

// parseExit returns the shell exit code from the result header, if any.
func parseExit(s string) *int {
	if code, ok := exitCodeValue(strings.TrimSpace(s)); ok {
		return &code
	}
	return nil
}

func exitCodeValue(s string) (int, bool) {
	const head = "[exit "
	if !strings.HasPrefix(s, head) {
		return 0, false
	}
	rest := s[len(head):]
	end := strings.IndexAny(rest, " ·]")
	if end <= 0 || rest[:end] == "none" {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

// formatDuration renders a tool wall time.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// formatSize renders characters the way the tern example does (1.2k).
func formatSize(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// clipRunes bounds s to n characters on a rune boundary.
func clipRunes(s string, n int) string {
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
