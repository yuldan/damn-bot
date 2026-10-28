package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

var (
	sgrRe   = regexp.MustCompile(`\x1b\[([0-9;]*)m`)
	emojiRe = regexp.MustCompile(`[\x{1F000}-\x{1FAFF}\x{FE0F}]`)
)

// sgrScan returns the unique color codes, any background code, and whether a
// blink/underline attribute appears.
func sgrScan(view string) (colors map[string]bool, bg map[string]bool, decor bool) {
	colors, bg = map[string]bool{}, map[string]bool{}
	for _, m := range sgrRe.FindAllStringSubmatch(view, -1) {
		params := strings.Split(m[1], ";")
		for i := 0; i < len(params); i++ {
			p := params[i]
			switch {
			case p == "38" && i+2 < len(params) && params[i+1] == "5":
				colors[params[i+2]] = true
				i += 2
			case p == "48" && i+2 < len(params) && params[i+1] == "5":
				bg[params[i+2]] = true
				i += 2
			case p == "4" || p == "5" || p == "21":
				decor = true
			}
		}
	}
	return colors, bg, decor
}

func designFrame(t *testing.T, state string) *Model {
	t.Helper()
	m, clk, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	at := clk.Now()
	if state != "empty" {
		m.appendBlock(&Block{Kind: BlockUser, Text: "refactor the retry path", At: at})
		m.appendBlock(&Block{Kind: BlockAssistant, Text: "reading the provider first", At: at})
		m.appendBlock(&Block{
			Kind: BlockTool, ToolName: "read_file", Args: `{"path":"internal/provider/http.go"}`,
			Tag: "3f9a12c0", Status: ToolOK, Duration: 120 * time.Millisecond, Size: 18200,
			Spill: "/data/blobs/3f/aa", SpillSize: 18200, ActionID: 12, Text: "ok", At: at,
		})
	}
	if state == "approval" {
		m.Update(ApprovalMsg{Request: ApprovalRequest{
			Tool: "shell", Preview: "+ rm -rf /tmp/x", Effect: "non_idempotent", NoSession: true,
		}, Reply: make(chan ApprovalDecision, 1)})
	}
	return m
}

func TestFramePaletteBudget(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"empty", "tool", "approval"} {
		if reason := paletteViolation(designFrame(t, state).View().Content, state == "approval"); reason != "" {
			t.Fatalf("%s frame: %s", state, reason)
		}
	}
}

// paletteViolation is the budget check; negative control cases feed synthetic
// frames with an extra color, a background outside the modal, and blink.
func paletteViolation(view string, allowBg bool) string {
	approved := map[string]bool{"252": true, "244": true, "238": true, "39": true, "108": true, "179": true, "167": true, "236": true}
	colors, bg, decor := sgrScan(view)
	if len(colors)+len(bg) > 6 {
		return "more than 6 SGR colors"
	}
	for c := range colors {
		if !approved[c] {
			return "unapproved color " + c
		}
	}
	if !allowBg && len(bg) > 0 {
		return "background outside the modal"
	}
	for c := range bg {
		if c != "236" {
			return "unapproved background " + c
		}
	}
	if decor {
		return "blink/underline"
	}
	return ""
}

func TestPaletteBudgetNegativeControls(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, frame string }{
		{"extra color", "\x1b[38;5;200mx"},
		{"background", "\x1b[48;5;236mx"},
		{"blink", "\x1b[5mx"},
		{"underline", "\x1b[4mx"},
		{"too many", "\x1b[38;5;252ma\x1b[38;5;39mb\x1b[38;5;108mc\x1b[38;5;179md\x1b[38;5;167me\x1b[38;5;238mf\x1b[38;5;244mg"},
	}
	for _, tc := range cases {
		if paletteViolation(tc.frame, false) == "" {
			t.Fatalf("%s was not rejected", tc.name)
		}
	}
	if !emojiRe.MatchString("emoji 😀") {
		t.Fatalf("emoji regex missed a positive sample")
	}
}

func TestNoEmoji(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"empty", "tool", "approval"} {
		if loc := emojiRe.FindString(designFrame(t, state).View().Content); loc != "" {
			t.Fatalf("%s frame contains emoji %q", state, loc)
		}
	}
}

func spacingModel(t *testing.T) *Model {
	t.Helper()
	m, clk, _ := newTestApp(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	at := clk.Now()
	m.appendBlock(&Block{Kind: BlockUser, Text: "first turn", At: at})
	m.appendBlock(&Block{Kind: BlockAssistant, Text: "a very long assistant line that will need truncation because it exceeds the left column width by a lot", At: at})
	m.appendBlock(&Block{
		Kind: BlockTool, ToolName: "read_file", Args: `{"path":"internal/provider/http.go"}`,
		Status: ToolOK, Text: "ok", At: at,
	})
	m.appendBlock(&Block{Kind: BlockUser, Text: "second turn", At: at})
	m.appendBlock(&Block{Kind: BlockAssistant, Text: "done", At: at})
	return m
}

func TestSpacingContract(t *testing.T) {
	t.Parallel()
	m := spacingModel(t)
	view := canonicalView(m.View().Content)
	raw := sgrRe.ReplaceAllString(m.View().Content, "")
	if !strings.Contains(raw, strings.Repeat("─", 100)) {
		t.Fatalf("missing thin separator:\n%s", raw)
	}
	left := checkPanel(t, raw)
	checkTurnSpacing(t, left, view)
	if !strings.Contains(view, "\n  internal/provider/http.go") {
		t.Fatalf("tool args missing two-space indent:\n%s", view)
	}
	if !strings.Contains(view, "…") {
		t.Fatalf("long line was not truncated with an ellipsis:\n%s", view)
	}
	goldenCompare(t, "spacing", view)
}

func checkPanel(t *testing.T, raw string) []string {
	t.Helper()
	var left []string
	for line := range strings.SplitSeq(raw, "\n") {
		before, after, ok := strings.Cut(line, "│")
		if !ok {
			continue
		}
		if got := len([]rune(before)); got != 71 {
			t.Fatalf("gutter at column %d, want 71: %q", got, line)
		}
		if got := len([]rune(after)); got != 28 {
			t.Fatalf("panel width = %d, want 28: %q", got, after)
		}
		left = append(left, strings.TrimRight(before, " "))
	}
	if len(left) == 0 {
		t.Fatalf("no side panel/gutter:\n%s", raw)
	}
	return left
}

func checkTurnSpacing(t *testing.T, left []string, view string) {
	t.Helper()
	for i := range left {
		if strings.Contains(left[i], "you> second turn") && i > 0 && left[i-1] == "" {
			return
		}
	}
	t.Fatalf("missing one blank line between turns:\n%s", view)
}

func goldenCompare(t *testing.T, name, view string) {
	t.Helper()
	path := filepath.Join("testdata", "r7", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { // #nosec G301 -- testdata
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(view), 0o600); err != nil { // #nosec G306 -- golden
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) // #nosec G304 -- golden under testdata
	if err != nil {
		t.Fatalf("%s golden: %v (UPDATE_GOLDEN=1)", name, err)
	}
	if view != canonicalView(string(want)) {
		t.Fatalf("%s snapshot mismatch\ngot:\n%s\nwant:\n%s", name, view, want)
	}
}
