package redact

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// sample is one positive corpus entry: text containing a secret of type typ;
// secret is the substring that must not survive redaction.
type sample struct {
	typ, rule, text, secret string
}

const corpusFile = "testdata/gitleaks-v8.28.0.tsv"

// loadCorpus reads the gitleaks-derived corpus (see the file header).
func loadCorpus(t testing.TB) []sample {
	t.Helper()
	f, err := os.Open(corpusFile)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close corpus: %v", err)
		}
	}()
	var out []sample
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 4 {
			t.Fatalf("corpus line %q: want 4 columns", line)
		}
		text, err1 := strconv.Unquote(cols[2])
		secret, err2 := strconv.Unquote(cols[3])
		if err1 != nil || err2 != nil {
			t.Fatalf("corpus line %q: unquote: %v %v", line, err1, err2)
		}
		unmark := func(s string) string { return strings.ReplaceAll(s, "«»", "") }
		out = append(out, sample{typ: cols[0], rule: cols[1], text: unmark(text), secret: unmark(secret)})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	return out
}

const (
	csAlnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	csTok   = csAlnum + "_-"
	csHex   = "0123456789abcdef"
	csAWS   = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	csDigit = "0123456789"
)

// newRNG returns a seeded generator for reproducible test data.
func newRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // G404: deterministic test data, not secrets
}

func randString(rng *rand.Rand, n int, cs string) string {
	b := make([]byte, n)
	for k := range b {
		b[k] = cs[rng.IntN(len(cs))]
	}
	return string(b)
}

// syntheticSecrets returns n seeded secrets cycling over every type,
// including known secret values (typ "secret").
func syntheticSecrets(n int) []sample {
	rng := newRNG(0x64616d6e)
	rs := func(k int, cs string) string { return randString(rng, k, cs) }
	tokens := []struct{ typ, prefix, cs string }{
		{"openai", "sk-", csTok},
		{"openrouter", "sk-or-v1-", csHex},
		{"github", "ghp_", csAlnum},
		{"github_pat", "github_pat_", csAlnum + "_"},
		{"aws", "AKIA", csAWS},
		{"slack", "xoxb-", csAlnum + "-"},
		{"stripe", "rk_live_", csAlnum},
		{"google", "AIza", csTok},
		{"gitlab", "glpat-", csTok},
		{"huggingface", "hf_", csAlnum},
		{"npm", "npm_", csAlnum},
	}
	special := []func() sample{
		func() sample { s := rs(35, csTok); return sample{"telegram", "", rs(10, csDigit) + ":" + s, s} },
		func() sample { s := rs(32, csTok); return sample{"bearer", "", "Authorization: Bearer " + s, s} },
		func() sample {
			s := "eyJ" + rs(20, csTok) + "." + rs(40, csTok) + "." + rs(43, csTok)
			return sample{"jwt", "", s, s}
		},
		func() sample {
			s := rs(18, csAlnum)
			return sample{"url_password", "", "mysql://root:" + s + "@10.0.0.7:3306/x", s}
		},
		func() sample {
			body := base64.StdEncoding.EncodeToString([]byte(rs(300, csAlnum)))
			return sample{"private_key", "", "-----BEGIN PRIVATE KEY-----\n" + body + "\n-----END PRIVATE KEY-----", body[100:164]}
		},
		func() sample { s := rs(8+rng.IntN(40), csTok+"!@#%"); return sample{TypeSecret, "", s, s} },
	}
	out := make([]sample, 0, n)
	for k := range n {
		if k%(len(tokens)+len(special)) < len(tokens) {
			g := tokens[k%len(tokens)]
			s := g.prefix + rs(64+rng.IntN(40), g.cs) // ≥ every pattern's minimum body
			out = append(out, sample{typ: g.typ, text: s, secret: s})
			continue
		}
		out = append(out, special[k%len(special)]())
	}
	return out
}

// knownSecrets collects the values of typ "secret" samples.
func knownSecrets(sets ...[]sample) []string {
	var out []string
	for _, set := range sets {
		for _, s := range set {
			if s.typ == TypeSecret {
				out = append(out, s.secret)
			}
		}
	}
	return out
}

// channel is one framing in which a secret reaches a sink (§14.3: model,
// DB, log, export, gateway, stream UI).
type channel struct {
	name string
	wrap func(string) string
}

func channels() []channel {
	return []channel{
		{"plain", func(s string) string { return s }},
		{"json", func(s string) string { return `{"error":{"message":` + strconv.Quote(s) + `,"code":401}}` }},
		{"logline", func(s string) string {
			return "time=2026-10-18T10:00:00Z level=WARN msg=retry detail=" + s + " attempt=2"
		}},
		{"url", func(s string) string { return "https://hooks.example.com/cb?state=xyz&v=" + s + "&next=%2Fhome" }},
		{"multiline", func(s string) string { return "Here is the config:\n\n```\n" + s + "\n```\nDone." }},
		{"error", func(s string) string {
			return fmt.Sprintf("provider openai: status 401: invalid credentials: %s (request id req_7Hq)", s)
		}},
	}
}

// leaked reports whether out still carries s or its last 12 bytes.
func leaked(out []byte, s string) bool {
	if bytes.Contains(out, []byte(s)) {
		return true
	}
	return len(s) >= 16 && bytes.Contains(out, []byte(s[len(s)-12:]))
}

// negativeCorpus returns look-alike strings that must not be redacted,
// grouped by category.
func negativeCorpus() map[string][]string {
	rng := newRNG(0x6e6567)
	raw := func(n int) []byte {
		b := make([]byte, n+8)
		for k := 0; k < n; k += 8 {
			binary.LittleEndian.PutUint64(b[k:], rng.Uint64())
		}
		return b[:n]
	}
	out := map[string][]string{}
	for range 60 {
		out["git_sha1"] = append(out["git_sha1"], hex.EncodeToString(raw(20)))
		out["sha256"] = append(out["sha256"], hex.EncodeToString(raw(32)))
		u := hex.EncodeToString(raw(16))
		out["uuid"] = append(out["uuid"], u[:8]+"-"+u[8:12]+"-4"+u[13:16]+"-a"+u[17:20]+"-"+u[20:])
		out["base64"] = append(out["base64"], base64.StdEncoding.EncodeToString(raw(9+rng.IntN(60))))
		out["base64url"] = append(out["base64url"], base64.RawURLEncoding.EncodeToString(raw(9+rng.IntN(60))))
	}
	out["lookalike"] = []string{
		"sk-short", "ghp_tooShort123", "akiaiosfodnn7example", "AKIA1234", "xoxb-123", "eyJhbGciOiJIUzI1NiJ9",
		"eyJabcdefgh.eyJabcdefgh", "hf_hub_download", "npm_config_cache", "glpat-short", "AIzaShort",
		"https://example.com:8443/path", "http://localhost:8080/", "ssh://git@github.com/acme/x.git",
		"12:30:45", "1234567:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi", "Authorization: Basic", "Bearer of bad news",
		"-----BEGIN CERTIFICATE-----\nMIIBszCCAVmgAwIBAgIU\n-----END CERTIFICATE-----", "-----BEGIN PUBLIC KEY-----",
		"sk_live_short", "rk_test", "github_pat_short", "postgres://localhost/db", "mailto:ops@example.com",
	}
	// SPEC §14.3 patterns are unanchored, so long hyphenated slugs that
	// contain "sk-" are redacted: reported, not gated.
	out["slug"] = []string{
		"risk-assessment-framework-guide", "/blog/task-management-best-practices", "desk-booking-system-v2-final",
		"kiosk-mode-configuration-reference", "ask-me-anything-session-recording",
	}
	return out
}

// Natural en/id response corpus for latency and benchmarks (P17, ADR-081).
func naturalResponses(n int) []string {
	en := strings.Fields("the a model returns answer quickly because each request is small and we can stream tokens " +
		"to user interface while tool output arrives later please check configuration file before deploy " +
		"it should work on linux macos and windows with same behavior data is stored locally")
	id := strings.Fields("saya akan menjelaskan langkah berikut dengan singkat agar mudah dipahami oleh tim kita " +
		"silakan periksa berkas konfigurasi sebelum menjalankan perintah ini karena data disimpan secara lokal " +
		"hasilnya cukup cepat dan aman untuk dipakai sehari hari")
	rng := newRNG(0x6e6174)
	out := make([]string, n)
	for k := range out {
		words := en
		if k%2 == 1 {
			words = id
		}
		var b strings.Builder
		for s := range 4 + rng.IntN(12) {
			if s > 0 {
				b.WriteString(" ")
			}
			for w := range 6 + rng.IntN(14) {
				word := words[rng.IntN(len(words))]
				if w == 0 {
					word = strings.ToUpper(word[:1]) + word[1:]
				} else {
					b.WriteByte(' ')
				}
				b.WriteString(word)
			}
			b.WriteString([...]string{".", ".", "?", ":\n- item 1\n- item 2\n", ".\n\n"}[rng.IntN(5)])
		}
		out[k] = b.String()
	}
	return out
}
