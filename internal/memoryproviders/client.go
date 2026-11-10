package memoryproviders

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/redact"
)

// Client applies the shared limits to one provider. It is safe for concurrent
// use when the provider is.
type Client struct {
	provider Provider
	maxToken int
	maxBytes int64
	limit    int
	tokens   TokenCounter
	red      *redact.Redactor
}

// NewClient wraps p with the framework bounds. A nil provider fails closed.
func NewClient(p Provider, s *Settings) (*Client, error) {
	if p == nil || s == nil {
		return nil, fmt.Errorf("%w: nil provider", ErrInvalid)
	}
	maxToken, err := lowerCap("max_tokens", s.MaxTokens, MaxTokens)
	if err != nil {
		return nil, err
	}
	maxBytes, err := lowerCap64("max_upload_bytes", s.MaxUploadBytes, MaxUploadBytes)
	if err != nil {
		return nil, err
	}
	limit := s.QueryLimit
	if limit == 0 {
		limit = DefaultQueryLimit
	}
	if limit < 1 || limit > MaxQueryLimit {
		return nil, fmt.Errorf("%w: query_limit must be 1..%d", ErrInvalid, MaxQueryLimit)
	}
	tokens := s.Tokens
	if tokens == nil {
		tokens = EstimateTokens
	}
	secrets := append([]string{s.Token}, s.KnownSecrets...)
	return &Client{
		provider: p, maxToken: maxToken, maxBytes: maxBytes, limit: limit,
		tokens: tokens, red: redact.New(redact.Options{Secrets: secrets}),
	}, nil
}

func lowerCap(name string, value, ceiling int) (int, error) {
	if value < 0 || value > ceiling {
		return 0, fmt.Errorf("%w: %s must be 0..%d", ErrInvalid, name, ceiling)
	}
	if value == 0 {
		return ceiling, nil
	}
	return value, nil
}

func lowerCap64(name string, value, ceiling int64) (int64, error) {
	if value < 0 || value > ceiling {
		return 0, fmt.Errorf("%w: %s must be 0..%d", ErrInvalid, name, ceiling)
	}
	if value == 0 {
		return ceiling, nil
	}
	return value, nil
}

// Recall runs one bounded recall. Hits keep the provider order, empty text is
// dropped, source labels are sanitised and the total never exceeds the token
// cap. A timeout or provider error is returned as a typed, redacted error.
func (c *Client) Recall(ctx context.Context, query string) ([]Snippet, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	hits, err := c.provider.Recall(ctx, Query{Text: query, Limit: c.limit})
	if err != nil {
		return nil, c.classify(ctx, err)
	}
	return c.bound(hits), nil
}

// Store uploads one item. The 1 MiB cap is checked before the provider sees
// the text; the operation shares the same 1.5 s deadline.
func (c *Client) Store(ctx context.Context, item Item) error {
	if strings.TrimSpace(item.Text) == "" {
		return fmt.Errorf("%w: empty item", ErrInvalid)
	}
	if int64(len(item.Text)) > c.maxBytes {
		return fmt.Errorf("%w: %d bytes > %d", ErrTooLarge, len(item.Text), c.maxBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	if err := c.provider.Store(ctx, item); err != nil {
		return c.classify(ctx, err)
	}
	return nil
}

// classify turns a provider failure into a typed, secret-free error. The
// provider name is safe to report; the cause text is redacted.
func (c *Client) classify(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s after %s", ErrTimeout, c.provider.Name(), DefaultTimeout)
	}
	if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrInvalid) {
		return err
	}
	return fmt.Errorf("%w: %s: %s", ErrResponse, c.provider.Name(), c.red.String(err.Error()))
}

// bound trims hits to the token cap. A hit that does not fit whole is clipped
// at a rune boundary; the next hit is not considered.
func (c *Client) bound(hits []Snippet) []Snippet {
	out := make([]Snippet, 0, len(hits))
	used := 0
	for _, h := range hits {
		text := strings.TrimSpace(h.Text)
		if text == "" {
			continue
		}
		n := max(1, c.tokens(text))
		remain := c.maxToken - used
		if remain <= 0 {
			break
		}
		if n > remain {
			text = truncateTokens(text, remain, c.tokens)
			if text == "" {
				break
			}
			n = max(1, c.tokens(text))
		}
		out = append(out, Snippet{Source: SourceLabel(h.Source), Text: text})
		used += n
		if used >= c.maxToken {
			break
		}
	}
	return out
}

func truncateTokens(s string, limit int, count TokenCounter) string {
	runes := []rune(s)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if max(1, count(string(runes[:mid]))) <= limit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return strings.TrimSpace(string(runes[:lo]))
}
