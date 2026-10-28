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

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[\x20-\x2f]*[\x40-\x7e]`)

// canonicalView is the explicit R7 golden normalization: strip ANSI CSI,
// normalize newlines, and trim trailing blanks per line. No other rewriting.
func canonicalView(s string) string {
	s = ansiSeq.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.Join(lines, "\n")
}

func goldenModel(t *testing.T, state string) *Model {
	t.Helper()
	m, clk, _ := newTestApp(t)
	m.input.Placeholder = ""
	m.input.Blur()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if state == "tool_spill" {
		at := clk.Now()
		m.appendBlock(&Block{Kind: BlockUser, Text: "find the bug", At: at})
		m.appendBlock(&Block{Kind: BlockAssistant, Text: "checking the provider", At: at})
		m.appendBlock(&Block{
			Kind: BlockTool, ToolName: "read_file", ToolCallID: "c1", Args: `{"path":"internal/provider/http.go"}`,
			Tag: "3f9a12c0", Status: ToolOK, Duration: 120 * time.Millisecond, Size: 18200,
			Spill: "/data/blobs/3f/aa", SpillSize: 18200, ActionID: 12, Text: "ok", At: at,
		})
	}
	if state == "queue" {
		at := clk.Now()
		m.appendBlock(&Block{Kind: BlockUser, Text: "start the turn", At: at})
		m.appendBlock(&Block{Kind: BlockAssistant, Text: "working on it", At: at})
		m.busy = true
		m.queue = []string{"follow up", "run tests"}
	}
	if state == "approval_modal" {
		m.Update(ApprovalMsg{
			Request: ApprovalRequest{
				Tool: "shell", Preview: "+ rm -rf /tmp/x\n  exit 1", Args: `{"command":"rm -rf /tmp/x"}`,
				Origin: "tui", Effect: "non_idempotent", NoSession: true,
			},
			Reply: make(chan ApprovalDecision, 1),
		})
	}
	return m
}

// TestR7GoldenViews compares the canonical snapshots byte-for-byte. Golden
// files are only written with UPDATE_GOLDEN=1; a failing test never rewrites.
func TestR7GoldenViews(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"empty_chat", "tool_spill", "approval_modal", "queue"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			got := canonicalView(goldenModel(t, state).View().Content)
			path := filepath.Join("testdata", "r7", state+".golden")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { // #nosec G301 -- testdata directory
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil { // #nosec G306 -- golden artifact
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path) // #nosec G304 -- golden under testdata
			if err != nil {
				t.Fatalf("golden %s: %v (run UPDATE_GOLDEN=1 go test -run TestR7GoldenViews to create it)", path, err)
			}
			if wantText := canonicalView(string(want)); got != wantText {
				t.Fatalf("R7 golden mismatch for %s\ngot:\n%s\nwant:\n%s", state, got, wantText)
			}
		})
	}
}
