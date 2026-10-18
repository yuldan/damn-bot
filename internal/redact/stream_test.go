package redact

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// chunks splits s into SSE-delta-sized pieces of 20–80 bytes.
func chunks(s string, rng *rand.Rand) [][]byte {
	var out [][]byte
	for len(s) > 0 {
		n := min(len(s), 20+rng.IntN(61))
		out = append(out, []byte(s[:n]))
		s = s[n:]
	}
	return out
}

// streamAll pushes parts through a fresh Stream and returns the output.
func streamAll(r *Redactor, parts ...[]byte) []byte {
	s := r.NewStream()
	var out []byte
	for _, p := range parts {
		out = s.Push(out, p)
	}
	return s.Flush(out)
}

// TestStreamRedactSplit: every corpus secret in context, split at every byte
// offset into 2 chunks (and at seeded random offsets into 3), streams to
// exactly the whole-blob redaction, with 0 leaks.
func TestStreamRedactSplit(t *testing.T) {
	t.Parallel()
	corpus, synth := loadCorpus(t), syntheticSecrets(60)
	r := New(Options{Secrets: knownSecrets(corpus, synth)})
	rng := newRNG(0x73706c6974)
	for _, s := range slices.Concat(corpus, synth) {
		text := []byte("Hasil: token " + s.text + ". Next step")
		want := r.Redact(text)
		check := func(how string, got []byte) {
			t.Helper()
			if !bytes.Equal(got, want) {
				t.Fatalf("%s %s: stream\n%q\nwant\n%q", s.typ, how, got, want)
			}
		}
		if leaked(want, s.secret) {
			t.Fatalf("%s: whole-blob leak %q", s.typ, want)
		}
		for k := range len(text) + 1 {
			check("split "+string(rune('0'+k%10)), streamAll(r, text[:k], text[k:]))
		}
		for range 50 {
			a := rng.IntN(len(text) + 1)
			b := a + rng.IntN(len(text)-a+1)
			check("3-split", streamAll(r, text[:a], text[a:b], text[b:]))
		}
	}
}

// TestStreamHoldCap: unbounded tails past the 4 KiB cap are redacted
// fail-closed and their remainder dropped; the hold stays bounded.
func TestStreamHoldCap(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("Ab0_", 2500) // 10 KB
	pemBody := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\n", 400)
	tests := []struct{ name, in, want string }{
		{"bearer", "Authorization: Bearer " + big + " end", "Authorization: Bearer [REDACTED:bearer] end"},
		{"telegram", "12345678:" + big + "\nok", "12345678:[REDACTED:telegram]\nok"},
		{"jwt", "x eyJ" + big + ".eyJabcdefgh.sig_sig_sig ok", "x [REDACTED:jwt] ok"},
		{
			"pem_end", "k -----BEGIN RSA PRIVATE KEY-----\n" + pemBody + "-----END RSA PRIVATE KEY-----\ntail sk-",
			"k [REDACTED:private_key]\ntail sk-",
		},
		{"pem_no_end", "-----BEGIN OPENSSH PRIVATE KEY-----\n" + pemBody + "-----END RSA", "[REDACTED:private_key]"},
	}
	r := New(Options{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := r.String(tc.in); got != tc.want {
				t.Errorf("Redact = %.200q, want %q", got, tc.want)
			}
			s := r.NewStream()
			var out []byte
			for c := range slices.Chunk([]byte(tc.in), 100) {
				out = s.Push(out, c)
				if len(s.buf) > r.hold+100 {
					t.Fatalf("hold %d bytes > cap %d", len(s.buf), r.hold+100)
				}
			}
			if got := string(s.Flush(out)); got != tc.want {
				t.Errorf("stream = %.200q, want %q", got, tc.want)
			}
		})
	}
}

// TestStreamHoldMinimal: text without a partial match is emitted by the Push
// that delivers it; only a possible secret start is held (ADR-081 butir 2).
func TestStreamHoldMinimal(t *testing.T) {
	t.Parallel()
	r := New(Options{Secrets: []string{"Zq9secretvalue"}})
	tests := []struct{ in, emitted string }{
		{"Halo dunia, ini jawaban kita.", "Halo dunia, ini jawaban kita."},
		{"pakai key gh", "pakai key "},
		{"lihat https:", "lihat https"},
		{"lihat https://user", "lihat https"},
		{"id 123456789:", "id 123456789"},
		{"Authorization: Bearer abc", ""},
		{"value Zq9s", "value "},
		{"-----BEGIN RSA PRIVATE KEY-----\nMII", ""},
		{"panjang sk-abc", "panjang "},
		{"token ghp_" + strings.Repeat("a", 36) + " ok", "token [REDACTED:github] ok"},
	}
	for _, tc := range tests {
		s := r.NewStream()
		if got := string(s.Push(nil, []byte(tc.in))); got != tc.emitted {
			t.Errorf("Push(%q) emitted %q, want %q", tc.in, got, tc.emitted)
		}
	}
}

func TestStreamZeroAlloc(t *testing.T) {
	r := New(Options{Secrets: []string{"Zq9secretvalue"}})
	rng := newRNG(12)
	parts := chunks(strings.Join(naturalResponses(4), " "), rng)
	s := r.NewStream()
	dst := make([]byte, 0, 4096)
	for _, p := range parts { // warm up internal buffer
		dst = s.Push(dst[:0], p)
	}
	allocs := testing.AllocsPerRun(20, func() {
		for _, p := range parts {
			dst = s.Push(dst[:0], p)
		}
	})
	if allocs != 0 {
		t.Errorf("Push allocs/run = %v, want 0", allocs)
	}
}

// TestStreamEdgeCases: the TestEdgeCases tables streamed in SSE-sized
// chunks give the same output, and streaming stays linear (EC-SEC-008).
func TestStreamEdgeCases(t *testing.T) {
	t.Parallel()
	r := edgeCaseRedactor()
	for id, tests := range edgeCases() {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			for _, tc := range tests {
				if got := string(streamAll(r, chunks(tc.in, newRNG(7))...)); got != tc.want {
					t.Errorf("stream(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
	t.Run("EC-SEC-008", func(t *testing.T) {
		r := linearRedactor()
		checkLinear(t, "stream", func(in []byte) {
			s := r.NewStream()
			var dst []byte
			for c := range slices.Chunk(in, 64) {
				dst = s.Push(dst[:0], c)
			}
			s.Flush(dst[:0])
		})
	})
}

// TestStreamKnownSecrets: the TestKnownSecrets table pushed as one chunk.
func TestStreamKnownSecrets(t *testing.T) {
	t.Parallel()
	r := New(Options{Secrets: knownSecretValues()})
	for _, tc := range knownSecretCases() {
		if got := string(streamAll(r, []byte(tc.in))); got != tc.want {
			t.Errorf("stream(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// FuzzRedactStream: for any surrounding bytes and chunking, a known secret
// never survives the stream, and the stream equals the whole-blob result.
func FuzzRedactStream(f *testing.F) {
	f.Add([]byte("pre "), []byte(" post"), uint64(1), uint8(3))
	f.Add([]byte("https://u:"), []byte("@host"), uint64(2), uint8(1))
	f.Add([]byte("Authorization: Bearer "), []byte("\n"), uint64(3), uint8(7))
	f.Add([]byte("-----BEGIN RSA PRIVATE KEY-----\n"), []byte("\n-----END RSA PRIVATE KEY-----"), uint64(4), uint8(2))
	f.Add([]byte("ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), []byte("sk-"), uint64(5), uint8(0))
	f.Add([]byte("123456789:"), []byte("eyJ"), uint64(6), uint8(9))
	f.Fuzz(func(t *testing.T, pre, post []byte, seed uint64, chunk uint8) {
		rng := newRNG(seed)
		secret := []byte(randString(rng, MinSecretLen+rng.IntN(32), csTok+"!@#"))
		r := New(Options{Secrets: []string{string(secret)}})
		in := slices.Concat(pre, secret, post)
		whole := r.Redact(in)
		got := streamAll(r, slices.Collect(slices.Chunk(in, 1+int(chunk%97)))...)
		if bytes.Contains(got, secret) || bytes.Contains(whole, secret) {
			t.Fatalf("secret %q leaked: stream %q whole %q", secret, got, whole)
		}
		if len(in) < holdCap && !bytes.Equal(got, whole) {
			t.Fatalf("stream %q != whole %q", got, whole)
		}
	})
}

// firstChunkLatencies measures, per natural en/id response chunked like SSE
// deltas, the time a fresh Stream adds to the first chunk and how many of its
// bytes it holds back (P17, ADR-081 butir 2: ≤ 1 ms p95).
func firstChunkLatencies(r *Redactor, responses []string) (lat []time.Duration, held []int) {
	rng := newRNG(1781)
	for _, resp := range responses {
		first := chunks(resp, rng)[0]
		s := r.NewStream()
		start := time.Now()
		out := s.Push(nil, first)
		lat = append(lat, time.Since(start))
		held = append(held, len(first)-len(out))
	}
	return lat, held
}

// TestFirstTokenLatency/redact: the redaction filter adds ≤ 1 ms p95 to the
// first token (ADR-081 butir 2), enforced through the timing gate (ADR-048).
func TestFirstTokenLatency(t *testing.T) {
	t.Parallel()
	t.Run("redact", testFirstTokenLatencyRedact)
}

func testFirstTokenLatencyRedact(t *testing.T) {
	var secrets []string
	rng := newRNG(34)
	for range 20 {
		secrets = append(secrets, randString(rng, 24, csTok))
	}
	lat, held := firstChunkLatencies(New(Options{Secrets: secrets}), naturalResponses(200))
	slices.Sort(lat)
	heldResponses := 0
	for _, h := range held {
		if h > 0 {
			heldResponses++
		}
	}
	t.Logf("first chunk: p50 %v, p95 %v, max %v over %d responses; %d hold ≥ 1 byte (max %d)",
		lat[len(lat)/2], lat[len(lat)*95/100], lat[len(lat)-1], len(lat), heldResponses, slices.Max(held))
	timing.Check(t, "redact first token p95 (ADR-081)", lat[len(lat)*95/100], time.Millisecond)
}

func BenchmarkStreamRedact(b *testing.B) {
	r := New(Options{Secrets: SecretsFromEnv([]string{"OPENAI_API_KEY=sk-proj-abcdefghijklmnop"})})
	text := benchText()
	parts := chunks(string(text), newRNG(56))
	s := r.NewStream()
	dst := make([]byte, 0, 256)
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		for _, p := range parts {
			dst = s.Push(dst[:0], p)
		}
		dst = s.Flush(dst[:0])
	}
}

// TestStreamUserinfoHoldCap: a userinfo candidate still open when the hold
// reaches 4 KiB (minified JSON after "://host:port", 64-byte chunks) is cut
// fail-closed without panicking, and nothing after the cut leaks until a
// separator ends the run (ADR-096 butir 6, 9; security review #14).
func TestStreamUserinfoHoldCap(t *testing.T) {
	t.Parallel()
	r := New(Options{})
	in := []byte(`{"u":"http://x:1",` + strings.Repeat(`"k":"v",`, 1024) + "} tail")
	s := r.NewStream()
	var out []byte
	for c := range slices.Chunk(in, 64) {
		out = s.Push(out, c)
	}
	out = s.Flush(out)
	if !bytes.Contains(out, []byte(SentinelPrefix+"url_password]")) || !bytes.HasSuffix(out, []byte(" tail")) {
		t.Fatalf("stream = %.120q … %q", out, out[max(0, len(out)-40):])
	}
	if bytes.Contains(out, []byte(`"k":"v"`)) {
		t.Fatalf("dropped run leaked: %.200q", out)
	}
}
