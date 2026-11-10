package assemble

import (
	"strings"
	"testing"
)

// TestReplyLanguageLine is GAP-INF-09: auto adds no line, a resolved language
// adds exactly one stable instruction of at most 12 tokens, and the rest of
// the stable tier is byte-identical.
func TestReplyLanguageLine(t *testing.T) {
	t.Parallel()
	base := Stable(StableOptions{})
	if strings.Contains(base, "Reply in ") {
		t.Fatal("auto carries a reply-language line")
	}
	for code, want := range map[string]string{
		"id":      "Reply in Indonesian (id).",
		"ja":      "Reply in Japanese (ja).",
		"zh-hant": "Reply in Chinese (Traditional) (zh-hant).",
	} {
		got := Stable(StableOptions{ReplyLanguage: code})
		_, rest, _ := strings.Cut(got, "\n\n")
		line, _, _ := strings.Cut(rest, "\n")
		if line != want {
			t.Errorf("%s line = %q, want %q", code, line, want)
		}
		if n := len(strings.Fields(line)); n > 12 {
			t.Errorf("%s line = %d tokens", code, n)
		}
		if strings.Replace(got, "\n\n"+want, "", 1) != base {
			t.Errorf("%s changed the rest of the stable tier", code)
		}
	}
}
