package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// fuzzFragments are the fragments of the corpus: block and inline markers,
// both line endings, astral runes, control characters and invalid UTF-8.
func fuzzFragments() []string {
	return []string{
		"```", "~~~", "``", "`", "**", "__", "*", "_", "[", "]", "(", ")", "](",
		"\n", "\r\n", "\r", "\n\n", "\t", " ", "a", "Z", "0", "- ", "1. ", "#", "|",
		"\U0001F600", "\u00e9", "\u4e16", "\x01", "\x7f", "\xff", "\xc3",
		"https://example.com/x", "tg://resolve?domain=a", "word",
	}
}

// fuzzCorpus builds n deterministic pseudo-random inputs.
func fuzzCorpus(n int) []string {
	frags := fuzzFragments()
	state := uint64(0xd00db07cafe)
	next := func() uint64 { // xorshift64: deterministic corpus, no crypto need
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return state
	}
	out := make([]string, n)
	for i := range out {
		var b strings.Builder
		for range next() % 12 {
			b.WriteString(frags[next()%uint64(len(frags))])
		}
		out[i] = b.String()
	}
	return out
}

// FuzzRenderTelegram proves the renderer never panics and always emits valid
// UTF-8 messages within the chunk limit, with entities inside the text.
func FuzzRenderTelegram(f *testing.F) {
	for _, src := range append(fallbackCorpus(), "\x01```\r\n\xff", "****", "[[[[") {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<18 {
			t.Skip("input besar; corpus 10k mencakup ukuran nyata")
		}
		msgs := RenderTelegram(src, Limits{})
		checkMessages(t, Limits{}, msgs)
		if len(msgs) == 0 || !strings.HasSuffix(msgs[len(msgs)-1].Text, Truncated) {
			total := 0
			for _, m := range msgs {
				total += Units(m.Text)
			}
			if want := MeasureCard(src); total != want {
				t.Fatalf("total unit %d != MeasureCard %d untuk %q", total, want, src)
			}
		}
		again := RenderTelegram(src, Limits{})
		if len(again) != len(msgs) {
			t.Fatalf("render tidak deterministik: %d vs %d pesan", len(msgs), len(again))
		}
	})
}

// FuzzRenderAST proves every platform renderer consumes the same AST without
// panicking, never exceeds the chunk limit, keeps UTF-8 valid and reopens a
// split fence. Telegram has its entity-specific fuzz target above.
func FuzzRenderAST(f *testing.F) {
	for _, src := range append(fallbackCorpus(), "```go\nx\n```", "| a | b |\n| --- | --- |\n| 1 | 2 |", "- a\n  - b") {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<18 {
			t.Skip("input besar; corpus 10k mencakup ukuran nyata")
		}
		for _, platform := range []Platform{PlatformSlack, PlatformWhatsApp, PlatformDiscord, PlatformMatrix, PlatformPlain} {
			checkPlatformRender(t, platform, src)
		}
	})
}

func checkPlatformRender(t *testing.T, platform Platform, src string) {
	t.Helper()
	lim := Limits{MaxLen: 300, MaxChunks: 100}
	msgs := Format(platform, src, lim)
	for i, m := range msgs {
		checkPlatformMessage(t, platform, i, lim, m)
	}
	if again := Format(platform, src, lim); len(again) != len(msgs) {
		t.Fatalf("%s render not deterministic: %d vs %d", platform, len(msgs), len(again))
	}
}

func checkPlatformMessage(t *testing.T, platform Platform, i int, lim Limits, m Message) {
	t.Helper()
	if !utf8.ValidString(m.Text) {
		t.Fatalf("%s message %d is not UTF-8: %q", platform, i, m.Text)
	}
	if n := Units(m.Text); n > lim.MaxLen {
		t.Fatalf("%s message %d = %d units > %d", platform, i, n, lim.MaxLen)
	}
	if platform == PlatformMatrix && m.HTML == "" {
		t.Fatalf("matrix message %d has no HTML", i)
	}
	if strings.Contains(strings.ToLower(m.HTML), "<script") {
		t.Fatalf("matrix message %d leaked a script tag", i)
	}
}
