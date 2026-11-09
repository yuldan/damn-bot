package memoryproviders

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Hard bounds of SPEC §9.10. Config may lower them, never raise them.
const (
	// MaxTokens is the shared retrieval cap (SPEC §4.5 row 6): local FTS and
	// external provider hits together never exceed 800 tokens.
	MaxTokens = 800
	// DefaultTokens is the per-operation timeout. A slower provider is an
	// event plus a WARN, never a stalled turn.
	DefaultTimeout = 1500 * time.Millisecond
	// MaxUploadBytes bounds one uploaded item at 1 MiB.
	MaxUploadBytes = 1 << 20
	// MaxRetries is the bounded retry budget of an idempotent recall; the
	// attempts share one timeout window.
	MaxRetries = 2
	// MaxQueryLimit bounds the number of hits requested from a provider.
	MaxQueryLimit = 20
	// DefaultQueryLimit is the requested hit count when config leaves it unset.
	DefaultQueryLimit = 5
)

// SourceHoncho is the only provider name in the closed registry.
const SourceHoncho = "honcho"

// Snippet is one provider hit. Source labels the <untrusted> wrapper and Text
// is data; the assembler wraps the raw Text itself, while other consumers use
// [Snippet.Wrapped].
type Snippet struct {
	Source string
	Text   string
}

// Wrapped renders the hit as one escaped <untrusted> block. The closing tag is
// neutralised case-insensitively so provider content cannot end the wrapper.
func (s Snippet) Wrapped() string {
	source := SourceLabel(s.Source)
	return `<untrusted source="` + source + `">` + EscapeClose(s.Text) + "</untrusted>"
}

// SourceLabel keeps [a-z0-9:_-] and lowercases ASCII; an empty result becomes
// "memory", so a provider cannot break out of the attribute.
func SourceLabel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ':', r == '_', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		}
	}
	if b.Len() == 0 {
		return "memory"
	}
	return b.String()
}

// EscapeClose neutralises every case variant of the closing untrusted tag.
func EscapeClose(s string) string {
	if !strings.Contains(strings.ToLower(s), "</untrusted") {
		return s
	}
	const closeTag = "</untrusted"
	var b strings.Builder
	b.Grow(len(s) + 8)
	for {
		lower := strings.ToLower(s)
		at := strings.Index(lower, closeTag)
		if at < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:at])
		b.WriteString(`<\/untrusted`)
		s = s[at+len(closeTag):]
	}
}

// Query is one recall request. Limit is clamped to 1..MaxQueryLimit.
type Query struct {
	Text  string
	Limit int
}

// Item is one bounded upload. Text is measured in bytes against
// [MaxUploadBytes] and must never contain a credential.
type Item struct {
	ID       string
	Text     string
	Metadata map[string]string
	At       time.Time
}

// Provider is the implementation seam. The framework client enforces timeout,
// caps, source labels and error redaction, so an implementation only speaks
// HTTP.
type Provider interface {
	Name() string
	Recall(ctx context.Context, q Query) ([]Snippet, error)
	Store(ctx context.Context, item Item) error
}

// Doer is the injected HTTP seam. A production wiring passes a netx Tool-lane
// client; tests pass an httptest-backed client. No package-level transport is
// created here.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Rewriter rewrites a recall query before it reaches the provider (MEM-009).
// The zero value means "no rewrite"; the feature is opt-in.
type Rewriter interface {
	Rewrite(ctx context.Context, query string) (string, error)
}

// TokenCounter measures text for the 800-token cap. The default is a
// conservative rune estimate; the assembler re-counts the final tier.
type TokenCounter func(string) int

// EstimateTokens is the default counter: one token per four runes, at least
// one token for non-empty text.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := utf8.RuneCountInString(s)
	return max(1, (n+3)/4)
}

// Settings is shared by the framework client and the provider factory.
type Settings struct {
	// HTTP is required.
	HTTP Doer
	// BaseURL is the provider endpoint; Honcho defaults to the public API.
	BaseURL string
	// Token is the resolved credential. It is never written to config or a
	// log; the client registers it with the error redactor.
	Token string
	// Workspace and SessionID select the provider-side namespace.
	Workspace string
	SessionID string
	// PeerID is the Honcho peer that authors stored messages.
	PeerID string
	// Rewriter is optional (MEM-009).
	Rewriter Rewriter
	// Retries bounds idempotent recall retries (0..MaxRetries). The attempts
	// share the framework's 1.5 s deadline.
	Retries int
	// Sleep is the retry backoff seam; nil uses a context-aware timer.
	Sleep func(context.Context, time.Duration) error
	// KnownSecrets are redacted from every error text.
	KnownSecrets []string
	// MaxTokens, MaxUploadBytes and QueryLimit may lower the framework caps.
	MaxTokens      int
	MaxUploadBytes int64
	QueryLimit     int
	// Tokens overrides the token estimator in tests.
	Tokens TokenCounter
}
