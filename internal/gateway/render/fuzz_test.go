package render

import (
	"strings"
	"testing"
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
