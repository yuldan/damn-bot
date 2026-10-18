package redact

import (
	"math"
	"strings"
	"testing"
	"time"
)

// edgeCaseRedactor holds the known secrets of the EC-SEC-005 provider
// headers (env values selected by SecretsFromEnv).
func edgeCaseRedactor() *Redactor {
	env := []string{"ACME_API_KEY=acme-live-7f3k2m9q", "X_CUSTOM_TOKEN=ctok-55aa66bb77cc"}
	return New(Options{Secrets: SecretsFromEnv(env)})
}

// edgeCases: EDGE-CASES-hermes entries exercisable at the redaction layer,
// checked whole-blob here and streamed in TestStreamEdgeCases.
func edgeCases() map[string][]struct{ in, want string } {
	key := "sk-proj-" + strings.Repeat("Ab3_", 12)
	return map[string][]struct{ in, want string }{
		// Provider error text that echoes the key is redacted before it
		// reaches the user.
		"EC-SEC-001": {{
			`openai: 401 {"error":{"message":"Incorrect API key provided: ` + key + `."}}`,
			`openai: 401 {"error":{"message":"Incorrect API key provided: [REDACTED:openai]."}}`,
		}},
		// Custom header values from provider entries are known secrets.
		"EC-SEC-005": {
			{"X-Api-Key: acme-live-7f3k2m9q\r\nX-Custom-Token: ctok-55aa66bb77cc", "X-Api-Key: [REDACTED:secret]\r\nX-Custom-Token: [REDACTED:secret]"},
			{`headers={"x-api-key": "acme-live-7f3k2m9q"}`, `headers={"x-api-key": "[REDACTED:secret]"}`},
		},
		// Debug dumps redact Authorization in every common rendering.
		"EC-SEC-007": {
			{"Authorization: Bearer None", "Authorization: Bearer [REDACTED:bearer]"},
			{"map[Authorization:[Bearer " + key + "]]", "map[Authorization:[Bearer [REDACTED:bearer]"},
			{`{'authorization': 'Bearer abc.def'}`, `{'authorization': 'Bearer [REDACTED:bearer]'}`},
			{`"Authorization":"Basic dXNlcjpwYXNz"`, `"Authorization":"Basic [REDACTED:basic_auth]"`},
		},
	}
}

// TestEdgeCases covers EC-SEC-001, EC-SEC-005, EC-SEC-007 and EC-SEC-008
// (linear cost) on whole-blob redaction.
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	r := edgeCaseRedactor()
	for id, tests := range edgeCases() {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			for _, tc := range tests {
				if got := r.String(tc.in); got != tc.want {
					t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
	t.Run("EC-SEC-008", func(t *testing.T) {
		r := linearRedactor()
		checkLinear(t, "whole", func(in []byte) { r.Redact(in) })
	})
}

// linearRedactor and linearUnits drive EC-SEC-008: each unit repeated to
// 16 KiB and 256 KiB is a pathological input for one matcher.
func linearRedactor() *Redactor { return New(Options{Secrets: []string{"hunter2hunter2"}}) }

func linearUnits() []string {
	return []string{
		"1", "12345678:", "sk-", "eyJ", "eyJaaaaaaaa.", "ghp_", "authorization: bearer ", "Authorization:",
		"://a:", "://", "-----BEGIN RSA PRIVATE KEY-----\n", "-----END RSA PRIVATE KEY-----", "hunter2hu", "a",
	}
}

// checkLinear: pathological input costs the same per byte at 16 KiB and
// 256 KiB (≤ 4× per byte), i.e. no super-linear blow-up. The bound is
// relative, so it holds under -race and load.
func checkLinear(t *testing.T, name string, f func([]byte)) {
	t.Helper()
	for _, u := range linearUnits() {
		// Small and large are sampled back to back, 5 times; the lowest
		// large/small ratio counts, so a load burst on a shared runner hitting
		// one size only cannot fake super-linear cost (real blow-up shows in
		// every pair).
		ratio, small, large := math.Inf(1), 0.0, 0.0
		in16, in256 := repeatTo(u, 16<<10), repeatTo(u, 256<<10)
		for range 5 {
			s16, s256 := perByte(f, in16), perByte(f, in256)
			if s256/s16 < ratio {
				ratio, small, large = s256/s16, s16, s256
			}
		}
		t.Logf("%-8s %-36q %6.1f → %6.1f ns/B", name, u, small, large)
		if ratio > 4 {
			t.Errorf("%s %q: %.1f ns/B at 256 KiB vs %.1f at 16 KiB: super-linear", name, u, large, small)
		}
	}
}

func repeatTo(u string, n int) []byte {
	return []byte(strings.Repeat(u, n/len(u)+1)[:n])
}

// perByte is one sample of f in ns per input byte: f repeats until ≥ 20 ms
// have elapsed, so coarse clocks (Windows: 0.5–15.6 ms ticks) cannot round
// a small input to 0 ns.
func perByte(f func([]byte), in []byte) float64 {
	const sample = 20 * time.Millisecond
	n, start := 0, time.Now()
	for time.Since(start) < sample {
		f(in)
		n++
	}
	return float64(time.Since(start).Nanoseconds()) / float64(n*len(in))
}

func benchText() []byte {
	text := strings.Join(naturalResponses(40), "\n")
	return []byte(text[:4096])
}

func BenchmarkRedact(b *testing.B) {
	r := New(Options{Secrets: SecretsFromEnv([]string{"OPENAI_API_KEY=sk-proj-abcdefghijklmnop"})})
	text := benchText()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		r.Redact(text)
	}
}
