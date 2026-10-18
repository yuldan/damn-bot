// Package redact replaces secrets in text with [REDACTED:<type>] (SPEC §14.3,
// ADR-071 butir 2–4, ADR-081 butir 2).
//
// Sources are the built-in token patterns of §14.3 (plus the ADR-071
// additions and a few same-family prefixes taken from gitleaks) and the
// exact values of known secrets supplied by the caller: env values selected
// by [SecretsFromEnv], keychain/secrets.enc values and OAuth tokens.
//
// Every matcher is a hand-written, position-anchored scanner: the work per
// input byte is bounded, so pathological input (EC-SEC-008) is linear. The
// same matcher drives whole-blob redaction ([Redactor.Redact]) and the
// streaming filter ([Stream]), which therefore produce identical output for
// any chunking, as long as no single hold exceeds the 4 KiB cap.
package redact

import (
	"cmp"
	"slices"
	"strings"
)

// SentinelPrefix starts every replacement. Write arguments carrying it
// (old_string, new_string, new_text, write_file.content) must be rejected
// with invalid_args by the caller (ADR-071 butir 4, CODE-TOOLS §5.3).
const SentinelPrefix = "[REDACTED:"

// MinSecretLen is the shortest env value [SecretsFromEnv] selects (SPEC §14.3
// env rule: length ≥ 8). It does not apply to [Options.Secrets] (ADR-096).
const MinSecretLen = 8

// TypeSecret is the replacement type of known secret values.
const TypeSecret = "secret"

// Options configures [New].
type Options struct {
	// Secrets are exact known secret values: env values (see
	// [SecretsFromEnv]), keychain/secrets.enc entries referenced by config
	// and OAuth tokens. Every non-empty value is redacted, whatever its
	// length (fail-closed, ADR-096); env values are filtered by
	// [SecretsFromEnv] before they get here.
	Secrets []string
}

// Redactor is an immutable redaction rule set; it is safe for concurrent use.
type Redactor struct {
	pats    []pattern
	byFirst [256][]*pattern // patterns indexed by the first byte they can match
	// pairs is the set of (byte, next byte) pairs some pattern can start
	// with: a superset of the positions where matchAt does more than its
	// cheap second-byte reject (the skip memo may still reject some), so
	// scan skips the others without a call.
	pairs [1 << 16 / 64]uint64
	hold  int // longest pending suffix a Stream may hold
}

// New builds a Redactor from the built-in patterns and opts.Secrets. Known
// secrets are tried first, longest first, so the most specific match wins.
func New(opts Options) *Redactor {
	r := &Redactor{hold: holdCap}
	secrets := slices.Clone(opts.Secrets)
	slices.SortFunc(secrets, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	secrets = slices.Compact(secrets)
	for _, s := range secrets {
		if s == "" {
			continue
		}
		r.pats = append(r.pats, pattern{typ: TypeSecret, secret: true, steps: []step{lit(s)}})
		// A partially seen secret must always resolve before the hold cap.
		r.hold = max(r.hold, len(s)+1)
	}
	r.pats = append(r.pats, builtinPatterns()...)
	builtin := 0
	for k := range r.pats {
		p := &r.pats[k]
		p.idx = -1
		if !p.secret {
			p.idx, builtin = builtin, builtin+1
		}
		first := p.firstLit()
		p.first = first
		c := first.lit[0]
		r.byFirst[c] = append(r.byFirst[c], p)
		if first.fold && c != upper(c) {
			r.byFirst[upper(c)] = append(r.byFirst[upper(c)], p)
		}
	}
	r.indexPairs()
	return r
}

// indexPairs fills r.pairs from byFirst, mirroring matchAt's reject: a
// one-byte leading literal admits every next byte; a longer one admits its
// second byte (either case when folded).
func (r *Redactor) indexPairs() {
	for c0 := range 256 {
		for _, p := range r.byFirst[c0] {
			head := p.first.lit
			if len(head) == 1 {
				for c1 := range 256 {
					r.setPair(byte(c0), byte(c1))
				}
				continue
			}
			r.setPair(byte(c0), head[1])
			if p.first.fold {
				r.setPair(byte(c0), upper(head[1]))
			}
		}
	}
}

func (r *Redactor) setPair(c0, c1 byte) {
	k := uint16(c0)<<8 | uint16(c1)
	r.pairs[k/64] |= 1 << (k % 64)
}

// pair reports whether a pattern can start with c0 c1.
func (r *Redactor) pair(c0, c1 byte) bool {
	k := uint16(c0)<<8 | uint16(c1)
	return r.pairs[k/64]&(1<<(k%64)) != 0
}

// SecretsFromEnv returns the values of environ ("NAME=value" entries) whose
// name matches (?i)(key|token|secret|password|passwd|auth) and whose value
// is at least MinSecretLen bytes long (SPEC §14.3).
func SecretsFromEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		if ok && len(val) >= MinSecretLen && secretName(name) {
			out = append(out, val)
		}
	}
	return out
}

func secretName(name string) bool {
	n := strings.ToLower(name)
	for _, w := range [...]string{"key", "token", "secret", "password", "passwd", "auth"} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// Redact returns b with every secret replaced. When nothing matches it
// returns b itself without copying; otherwise a new slice. An unterminated
// PEM private key block is redacted to the end of b (fail-closed).
func (r *Redactor) Redact(b []byte) []byte {
	out, last, _, _ := r.scan(nil, b, 0, modeFinal)
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

// String is [Redactor.Redact] for strings; s is returned as is when nothing
// matches.
func (r *Redactor) String(s string) string {
	b := []byte(s)
	out, last, _, _ := r.scan(nil, b, 0, modeFinal)
	if out == nil {
		return s
	}
	return string(append(out, b[last:]...))
}

// ContainsSentinel reports whether s carries a redaction sentinel. Code tools
// reject write arguments for which it returns true (ADR-071 butir 4).
func ContainsSentinel(s string) bool {
	return strings.Contains(s, SentinelPrefix)
}
