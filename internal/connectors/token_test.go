package connectors

import (
	"context"
	"testing"
	"time"
)

// TestTokenCacheCalls proves a cached token is reused: two Token calls with a
// live token trigger exactly one refresh, which Calls reports.
func TestTokenCacheCalls(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	c := newTokenCache(func(context.Context) (token, error) {
		return token{Value: "tok", Expiry: now.Add(time.Hour)}, nil
	}, func() time.Time { return now }, nil)
	for range 2 {
		if got, err := c.Token(context.Background()); err != nil || got != "tok" {
			t.Fatalf("Token = %q, %v", got, err)
		}
	}
	if got := c.Calls(); got != 1 {
		t.Fatalf("Calls = %d, want 1", got)
	}
}
