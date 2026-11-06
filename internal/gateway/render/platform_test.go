package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestF7RenderGoldens is the GAP-GW-04 golden matrix: one AST, one renderer per
// platform, covering inline markup, a fenced code block, a GFM table and a
// nested list.
func TestF7RenderGoldens(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) { checkGolden(t, tc) })
	}
}

type goldenCase struct {
	name     string
	src      string
	want     map[Platform]string
	html     string
	contains string
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{
			name: "inline",
			src:  "**bold** and *italic* and `code`",
			want: map[Platform]string{
				PlatformSlack:    "*bold* and _italic_ and `code`",
				PlatformWhatsApp: "*bold* and _italic_ and ```code```",
				PlatformDiscord:  "**bold** and *italic* and `code`",
				PlatformPlain:    "bold and italic and code",
				PlatformMatrix:   "bold and italic and code",
			},
			html: "<b>bold</b> and <i>italic</i> and <code>code</code>",
		},
		{
			name: "fence",
			src:  "```go\nfmt.Println(1)\n```",
			want: map[Platform]string{
				PlatformSlack:    "```\nfmt.Println(1)\n```",
				PlatformWhatsApp: "```\nfmt.Println(1)\n```",
				PlatformDiscord:  "```go\nfmt.Println(1)\n```",
				PlatformPlain:    "fmt.Println(1)",
				PlatformMatrix:   "fmt.Println(1)",
			},
			contains: `class="language-go"`,
		},
		{
			name: "table",
			src:  "| A | B |\n| --- | --- |\n| 1 | 2 |",
			want: map[Platform]string{
				PlatformSlack:  "| A | B |\n| --- | --- |\n| 1 | 2 |",
				PlatformPlain:  "| A | B |\n| --- | --- |\n| 1 | 2 |",
				PlatformMatrix: "| A | B |\n| --- | --- |\n| 1 | 2 |",
			},
			contains: "<th>A</th>",
		},
		{
			name: "nested list",
			src:  "- a\n  - b\n- c",
			want: map[Platform]string{
				PlatformSlack:  "- a\n  - b\n- c",
				PlatformPlain:  "- a\n  - b\n- c",
				PlatformMatrix: "- a\n  - b\n- c",
			},
		},
	}
}

func checkGolden(t *testing.T, tc goldenCase) {
	t.Helper()
	doc, err := Parse(tc.src)
	if err != nil {
		t.Fatal(err)
	}
	for platform, want := range tc.want {
		msgs := ParseDoc(platform, doc, Limits{})
		if len(msgs) != 1 || msgs[0].Text != want {
			t.Errorf("%s = %q (%d msgs), want %q", platform, textOf(msgs), len(msgs), want)
		}
	}
	matrix := ParseDoc(PlatformMatrix, doc, Limits{})
	if tc.html != "" && matrix[0].HTML != tc.html {
		t.Errorf("matrix HTML = %q, want %q", matrix[0].HTML, tc.html)
	}
	if tc.contains != "" && !strings.Contains(matrix[0].HTML, tc.contains) {
		t.Errorf("matrix HTML = %q, want %q", matrix[0].HTML, tc.contains)
	}
}

func textOf(msgs []Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.Text)
	}
	return b.String()
}

// TestF7RenderChunkKeepsFence: a code block longer than one message is split
// with the fence reopened in every chunk and no body byte lost.
func TestF7RenderChunkKeepsFence(t *testing.T) {
	body := strings.Repeat("0123456789\n", 60)
	src := "```go\n" + body + "```"
	for _, platform := range []Platform{PlatformSlack, PlatformWhatsApp, PlatformDiscord, PlatformPlain, PlatformMatrix} {
		msgs := Format(platform, src, Limits{MaxLen: 120, MaxChunks: 1000})
		if len(msgs) < 2 {
			t.Fatalf("%s: chunks = %d, want >1", platform, len(msgs))
		}
		var rebuilt strings.Builder
		for i, m := range msgs {
			if Units(m.Text) > 120 {
				t.Fatalf("%s chunk %d = %d units", platform, i, Units(m.Text))
			}
			if !utf8.ValidString(m.Text) {
				t.Fatalf("%s chunk %d is not valid UTF-8", platform, i)
			}
			if strings.Count(m.Text, "```")%2 != 0 {
				t.Fatalf("%s chunk %d has an unbalanced fence: %q", platform, i, m.Text)
			}
			rebuilt.WriteString(trimFence(m.Text))
		}
		if got := strings.ReplaceAll(rebuilt.String(), "\n", ""); got != strings.ReplaceAll(body, "\n", "") {
			t.Fatalf("%s lost code body: got %d chars want %d", platform, len(got), len(strings.ReplaceAll(body, "\n", "")))
		}
	}
}

func trimFence(s string) string {
	s = strings.TrimPrefix(s, "```go\n")
	s = strings.TrimPrefix(s, "```\n")
	s = strings.TrimSuffix(s, "\n```")
	return s
}

// TestF7RenderMatrixEscapes proves the Matrix HTML subset escapes user text:
// markup in the model output can never become an active tag.
func TestF7RenderMatrixEscapes(t *testing.T) {
	msgs := Format(PlatformMatrix, "<script>alert(1)</script>", Limits{})
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d", len(msgs))
	}
	if strings.Contains(msgs[0].HTML, "<script") {
		t.Fatalf("HTML = %q, raw script tag survived", msgs[0].HTML)
	}
	if !strings.Contains(msgs[0].HTML, "&lt;script&gt;") {
		t.Fatalf("HTML = %q, want escaped script", msgs[0].HTML)
	}
}
