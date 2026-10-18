package redact

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

// TestRedaction is the §14.3 / ADR-071 corpus gate: every gitleaks-derived
// sample (≥ 1 per pattern, full PEM bodies) and 60 synthetic secrets, each in
// 6 channels, leave 0 secrets; the negative corpus reports false positives.
func TestRedaction(t *testing.T) {
	t.Parallel()
	corpus, synth := loadCorpus(t), syntheticSecrets(60)
	r := New(Options{Secrets: knownSecrets(corpus, synth)})

	t.Run("pattern_coverage", func(t *testing.T) {
		t.Parallel()
		seen := map[string]bool{}
		for _, s := range corpus {
			seen[s.typ] = true
		}
		for _, p := range r.pats {
			if !seen[p.typ] {
				t.Errorf("no corpus sample for pattern type %q", p.typ)
			}
		}
	})
	t.Run("gitleaks", func(t *testing.T) { t.Parallel(); checkChannels(t, r, corpus) })
	t.Run("synthetic", func(t *testing.T) { t.Parallel(); checkChannels(t, r, synth) })
	t.Run("negative", func(t *testing.T) { t.Parallel(); checkNegative(t, r) })
	t.Run("url_password_forms", func(t *testing.T) { t.Parallel(); checkURLPasswordForms(t, r) })
}

// checkURLPasswordForms (R-02, ADR-096 butir 6): empty user, '/' and '@' in
// the password and the JSON-escaped ":\/\/" form are redacted whole.
func checkURLPasswordForms(t *testing.T, r *Redactor) {
	t.Helper()
	const red = SentinelPrefix + "url_password]"
	tests := []struct{ in, want, secret string }{
		{"redis://:Sup3rS3cretPw@cache:6379/0", "redis://:" + red + "@cache:6379/0", "Sup3rS3cretPw"},
		{"postgres://app:pa/ss+w0rd@db", "postgres://app:" + red + "@db", "pa/ss+w0rd"},
		{"postgres://app:p@ss@db x", "postgres://app:" + red + "@db x", "p@ss"},
		{`{"u":"https:\/\/app:S3cretPw99@db"}`, `{"u":"https:\/\/app:` + red + `@db"}`, "S3cretPw99"},
		{`https:\/\/:S3cretPw98@db`, `https:\/\/:` + red + `@db`, "S3cretPw98"},
		{"ssh://git@github.com/x https://h:8443/p", "ssh://git@github.com/x https://h:8443/p", ""},
	}
	for _, tc := range tests {
		got := r.String(tc.in)
		if got != tc.want || (tc.secret != "" && strings.Contains(got, tc.secret)) {
			t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// checkChannels asserts 0 leaks for every sample in every channel and the
// sample's type name in the plain channel.
func checkChannels(t *testing.T, r *Redactor, samples []sample) {
	t.Helper()
	leaks := 0
	for _, s := range samples {
		for _, ch := range channels() {
			out := r.Redact([]byte(ch.wrap(s.text)))
			if leaked(out, s.secret) {
				leaks++
				t.Errorf("%s/%s (%s): secret leaked: %q", s.typ, ch.name, s.rule, out)
			}
			if want := SentinelPrefix + s.typ + "]"; ch.name == "plain" && !bytes.Contains(out, []byte(want)) {
				t.Errorf("%s/plain: want %s in %q", s.typ, want, out)
			}
		}
	}
	t.Logf("%d secrets × %d channels: %d leaks", len(samples), len(channels()), leaks)
}

// checkNegative reports false positives per category; only the "slug"
// category (a known consequence of unanchored §14.3 patterns) may have any.
func checkNegative(t *testing.T, r *Redactor) {
	t.Helper()
	total := 0
	for cat, items := range negativeCorpus() {
		fp := 0
		for _, s := range items {
			if got := r.String(s); got != s {
				fp++
				t.Logf("false positive (%s): %q -> %q", cat, s, got)
			}
		}
		total += len(items)
		t.Logf("negative %-9s: %d/%d false positives", cat, fp, len(items))
		if fp > 0 && cat != "slug" {
			t.Errorf("negative %s: %d false positives, want 0", cat, fp)
		}
	}
	if total < 200 {
		t.Errorf("negative corpus has %d strings, want ≥ 200", total)
	}
}

func TestSecretsFromEnv(t *testing.T) {
	t.Parallel()
	env := []string{
		"OPENAI_API_KEY=sk-12345678", "GITHUB_TOKEN=gho_abcdefgh", "DB_PASSWORD=hunter2hunter2", "MY_PASSWD=12345678",
		"CLIENT_SECRET=abcdefgh", "X_AUTH=auth-value", "SHORT_TOKEN=1234567", "HOME=/home/someone/very/long",
		"PATH=/usr/bin:/bin", "=C:=C:\\Windows", "NOEQUALS", "lower_key=lowercase-value",
	}
	want := []string{"sk-12345678", "gho_abcdefgh", "hunter2hunter2", "12345678", "abcdefgh", "auth-value", "lowercase-value"}
	if got := SecretsFromEnv(env); !slices.Equal(got, want) {
		t.Errorf("SecretsFromEnv = %q, want %q", got, want)
	}
}

// knownSecretValues feeds TestKnownSecrets (and its stream twin): duplicates,
// an empty value and a short one.
func knownSecretValues() []string {
	return []string{"short", "abcdefgh", "zyxwvuts", "abcdefghijkl", "abcdefgh", "", "wxyz-9876-qq"}
}

// knownSecretCases: expected redaction with knownSecretValues.
func knownSecretCases() []struct{ in, want string } {
	return []struct{ in, want string }{
		{"x abcdefghijkl y", "x [REDACTED:secret] y"}, // longest secret wins
		{"x abcdefgh y", "x [REDACTED:secret] y"},
		{"short goes", "[REDACTED:secret] goes"}, // no length floor for explicit secrets (ADR-096)
		// A secret glued to a token is covered whole, not cut at the token end.
		{"ghp_" + strings.Repeat("a", 36) + "wxyz-9876-qq!", "[REDACTED:github]!"},
		{"https://u:p@h abcdefgh", "https://u:[REDACTED:url_password]@h [REDACTED:secret]"},
		// Userinfo has no length ceiling: a 300-byte user / 3000-byte password
		// is still redacted (ADR-096).
		{
			"https://" + strings.Repeat("u", 300) + ":" + strings.Repeat("p", 3000) + "@h",
			"https://" + strings.Repeat("u", 300) + ":[REDACTED:url_password]@h",
		},
	}
}

func TestKnownSecrets(t *testing.T) {
	t.Parallel()
	r := New(Options{Secrets: knownSecretValues()})
	if n := slices.IndexFunc(r.pats, func(p pattern) bool { return !p.secret }); n != 5 {
		t.Errorf("%d known secret patterns, want 5 (deduplicated, non-empty)", n)
	}
	for _, tc := range knownSecretCases() {
		if got := r.String(tc.in); got != tc.want {
			t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	in := []byte("nothing to see")
	if out := r.Redact(in); &out[0] != &in[0] {
		t.Error("Redact copied input without a match")
	}
}

// TestSentinelGuard: the detection primitive behind the code-tools guard on
// write fields (ADR-071 butir 4, CODE-TOOLS §5.3): redacted text always
// carries the sentinel and is therefore refused as write input.
func TestSentinelGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		field, value string
		want         bool
	}{
		{"old_string", "token = [REDACTED:github]", true},
		{"new_string", "pw=[REDACTED:secret]\n", true},
		{"new_text", "[REDACTED:", true},
		{"write_file.content", "a\n[REDACTED:private_key]\nb", true},
		{"multi_edit", "[REDACTED]", false},
		{"plain", "REDACTED: nothing", false},
	}
	for _, tc := range tests {
		if got := ContainsSentinel(tc.value); got != tc.want {
			t.Errorf("%s: ContainsSentinel(%q) = %v, want %v", tc.field, tc.value, got, tc.want)
		}
	}
	corpus := loadCorpus(t)
	r := New(Options{Secrets: knownSecrets(corpus)})
	for _, s := range corpus {
		if ContainsSentinel(s.text) || !ContainsSentinel(r.String(s.text)) {
			t.Errorf("%s: sentinel not detected around redaction of %.40q", s.typ, s.text)
		}
	}
}
