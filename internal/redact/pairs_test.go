package redact

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// withoutPairs returns a copy of r whose pair prefilter admits everything:
// the scan behaviour before the prefilter existed.
func withoutPairs(r *Redactor) *Redactor {
	ref := *r
	for i := range ref.pairs {
		ref.pairs[i] = ^uint64(0)
	}
	return &ref
}

// FuzzPairPrefilter: the pair prefilter never changes output, whole-blob or
// streamed with any chunking, for built-in patterns and known secrets
// (incl. 1-byte ones and the "://"/":" anchors).
func FuzzPairPrefilter(f *testing.F) {
	for _, s := range []string{
		"Authorization: Bearer abcdefgh", "AUTHORIZATION:bearer x", "https://u:p@h", "redis://:pw@h/0",
		"123456789:" + strings.Repeat("A", 35), "ghp_" + strings.Repeat("a", 36), "-----BEGIN RSA PRIVATE KEY-----\nx",
		"eyJabcdefgh.abcdefgh.abcdefgh", "sk-", "a:b:c", "x",
	} {
		f.Add([]byte(s), "k", uint8(5))
	}
	f.Fuzz(func(t *testing.T, in []byte, secret string, chunk uint8) {
		r := New(Options{Secrets: []string{secret, "Q"}})
		ref := withoutPairs(r)
		if got, want := r.Redact(in), ref.Redact(in); !bytes.Equal(got, want) {
			t.Fatalf("whole %q: %q, want %q", in, got, want)
		}
		parts := slices.Collect(slices.Chunk(in, 1+int(chunk%17)))
		if got, want := streamAll(r, parts...), streamAll(ref, parts...); !bytes.Equal(got, want) {
			t.Fatalf("stream %q: %q, want %q", in, got, want)
		}
	})
}

func TestPairPrefilterSelective(t *testing.T) {
	t.Parallel()
	r := New(Options{})
	if r.pair('t', 'h') || !r.pair('s', 'k') || !r.pair('A', 'U') || !r.pair('a', 'u') {
		t.Error("pair table does not mirror the leading literals")
	}
}
