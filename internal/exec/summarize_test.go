package exec

import (
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/testutil/timing"
)

func summarize(t testing.TB, out string, inlineLeft int) (Summary, [][]byte) {
	t.Helper()
	code := 1
	res := &Result{Status: StatusExited, ExitCode: &code, Duration: 2341 * time.Millisecond, Runner: RunnerPipe, Output: []byte(out)}
	var blobs [][]byte
	s, err := NewSummarizer(redact.New(redact.Options{Secrets: []string{"hunter2-secret"}})).Summarize(res, inlineLeft,
		func(log, raw []byte) (string, error) {
			blobs = append(blobs, log, raw)
			return "blobs/ab/abcd", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return s, blobs
}

func TestSummarizePipeline(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct{ in, want string }{
		"ansi":      {"\x1b[31mred\x1b[0m \x1b]0;title\x07\x1bPdcs\x1b\\ok\n", "red ok"},
		"cr":        {"10%\r50%\r100%\ndone\r\n", "100%\ndone"},
		"backspace": {"abc\b\bX\n", "aX"},
		"invalid":   {"a\xffb\n", "a\uFFFDb"},
		"progress":  {"step 1/3\nstep 2/3\nstep 3/3\nok\n", "step 3/3 [×3]\nok"},
		"dedupe":    {"same\nsame\nsame\nother\n", "same [×3]\nother"},
		"redact":    {"token ghp_" + strings.Repeat("a", 36) + " pw hunter2-secret\n", "token [REDACTED:github] pw [REDACTED:secret]"},
		"controls":  {"a\x07b\x00c\x7f\u0085d\n", "abcd"},
	} {
		s, blobs := summarize(t, c.in, TurnInlineChars)
		if s.Text != c.want || s.Spilled || blobs != nil {
			t.Errorf("%s: %q, want %q", name, s.Text, c.want)
		}
	}
	s, _ := summarize(t, "x\n", TurnInlineChars)
	if s.Header != "[exit 1 · 2.341 ms · signal none · runner pipe · status exited]" {
		t.Errorf("header %q", s.Header)
	}
}

func TestSummarizeSpill(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("HEAD-LINE\n")
	for i := range 2000 {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(" filler\n")
		if i == 900 {
			b.WriteString("main.go:12:3: undefined: foo\nerror: build failed\nerror: build failed\n")
		}
	}
	b.WriteString("TAIL-LINE hunter2-secret\n")
	s, blobs := summarize(t, b.String(), TurnInlineChars)
	if !s.Spilled || len(blobs) != 2 {
		t.Fatalf("not spilled: %d blobs", len(blobs))
	}
	for _, want := range []string{"HEAD-LINE", "main.go:12:3: undefined: foo", "error: build failed", "TAIL-LINE [REDACTED:secret]", "[full output: blobs/ab/abcd]"} {
		if !strings.Contains(s.Text, want) {
			t.Errorf("preview lacks %q:\n%s", want, s.Text)
		}
	}
	if strings.Count(s.Text, "error: build failed") != 1 || s.CharsModel > 900 {
		t.Errorf("preview: %d chars\n%s", s.CharsModel, s.Text)
	}
	if strings.Contains(string(blobs[0]), "hunter2") || strings.Contains(string(blobs[1]), "hunter2") {
		t.Error("blob not redacted")
	}
	short, _ := summarize(t, b.String(), 100)
	if short.CharsModel > 500 || !strings.Contains(short.Text, "[full output:") {
		t.Errorf("short preview %d chars", short.CharsModel)
	}
}

// BenchmarkSummarize1MB (§A.9): the pipeline on 1 MiB of build-like output
// stays under 15 ms (×1.5 on shared runners, ADR-077).
func BenchmarkSummarize1MB(b *testing.B) {
	var sb strings.Builder
	for i := 0; sb.Len() < 1<<20; i++ {
		sb.WriteString("\x1b[32mcompiling\x1b[0m pkg/")
		sb.WriteString(strings.Repeat("a", i%13))
		sb.WriteString(" 42%\r 87%\r100%\n")
	}
	out := sb.String()[:1<<20]
	b.SetBytes(int64(len(out)))
	z := NewSummarizer(redact.New(redact.Options{}))
	code := 0
	res := &Result{Status: StatusExited, ExitCode: &code, Runner: RunnerPipe, Output: []byte(out)}
	for b.Loop() {
		if _, err := z.Summarize(res, TurnInlineChars, func(_, _ []byte) (string, error) { return "p", nil }); err != nil {
			b.Fatal(err)
		}
	}
	if b.N >= 100 {
		timing.Check(b, "summarize 1 MiB (§A.9)", b.Elapsed()/time.Duration(b.N), 15*time.Millisecond)
	}
}

// TestSummarizeUnterminatedEscape (EX47-1): one unterminated OSC/DCS
// cannot hide everything after it; at most maxEscape bytes are consumed
// and the following text still reaches the model.
func TestSummarizeUnterminatedEscape(t *testing.T) {
	t.Parallel()
	for _, esc := range []string{"\x1b]0;title", "\x1bP1;2q", "\x1b[9999"} {
		out := "head\n" + esc + strings.Repeat("A", maxEscape*2) + "\nvisible tail\n"
		s, _ := summarize(t, out, TurnInlineChars)
		if !strings.Contains(s.Text, "visible tail") {
			t.Errorf("%q: text after an unterminated escape was hidden", esc)
		}
	}
}

// TestSummarizeSplitSecretNotLeaked (EX47-2): an SGR code between two
// halves of a known secret must not leave the raw blob readable.
func TestSummarizeSplitSecretNotLeaked(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(strings.Repeat("filler ", 800))
	b.WriteString("\n")
	b.WriteString("value hunter2-\x1b[31msecret end\n")
	s, blobs := summarize(t, b.String(), InlineChars)
	if !s.Spilled || len(blobs) != 2 {
		t.Fatalf("not spilled: %d", len(blobs))
	}
	for k, blob := range blobs {
		if strings.Contains(string(blob), "hunter2-") {
			t.Errorf("blob %d leaks the split secret: %q", k, blob)
		}
	}
}
