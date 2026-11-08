package connectors

import (
	"context"
	"sync"
	"time"
)

// ResolveSecret returns a non-empty secret or a typed auth error. An empty
// name means the connector is not configured and is reported as unavailable,
// not as a missing credential.
func ResolveSecret(src SecretSource, name string) (string, error) {
	if src == nil || name == "" {
		return "", &Error{Code: codeModuleUnavailable, Message: "connector credential is not configured"}
	}
	value, err := src(name)
	if err != nil || value == "" {
		return "", &Error{Code: codeAuthRequired, Message: "secret " + name + " is unavailable", err: err}
	}
	return value, nil
}

// token is one cached access token with its absolute expiry.
type token struct {
	Value  string
	Expiry time.Time
}

// tokenCache is the OAuth/tenant-token cache shared by the connectors of one
// module. A token is reused until its expiry minus the 60 s skew, then the
// refresh function runs once; concurrent callers wait on the same mutex
// instead of stampeding the token endpoint.
type tokenCache struct {
	mu      sync.Mutex
	tok     token
	refresh func(context.Context) (token, error)
	now     func() time.Time
	red     *Redactor
	calls   int
}

// newTokenCache builds a cache. red receives every acquired token so a later
// error cannot echo it.
func newTokenCache(refresh func(context.Context) (token, error), now func() time.Time, red *Redactor) *tokenCache {
	if now == nil {
		now = time.Now
	}
	return &tokenCache{refresh: refresh, now: now, red: red}
}

// Token returns a valid access token, refreshing when the cached one is
// missing or within the skew window.
func (c *tokenCache) Token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", &Error{Code: codeTimeout, Message: "token request cancelled", err: err}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok.Value != "" && c.now().Add(internalTokenSkew).Before(c.tok.Expiry) {
		return c.tok.Value, nil
	}
	if c.refresh == nil {
		return "", &Error{Code: codeAuthRequired, Message: "no token source configured"}
	}
	tok, err := c.refresh(ctx)
	if err != nil {
		return "", err
	}
	if tok.Value == "" {
		return "", &Error{Code: codeAuthRequired, Message: "token endpoint returned an empty token"}
	}
	if tok.Expiry.IsZero() {
		tok.Expiry = c.now().Add(time.Minute)
	}
	c.tok = tok
	c.calls++
	if c.red != nil {
		c.red.Add(tok.Value)
	}
	return tok.Value, nil
}

// Invalidate drops the cached token after a 401 so the next call refreshes.
func (c *tokenCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tok = token{}
}

// Calls reports how many refreshes happened; tests use it to prove caching.
func (c *tokenCache) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// oauthToken is one token-endpoint response in the common OAuth shape.
type oauthToken struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

// expiryOf converts expires_in seconds into an absolute expiry with a
// conservative floor.
func expiryOf(now time.Time, expiresIn int) time.Time {
	if expiresIn <= 0 {
		expiresIn = 60
	}
	return now.Add(time.Duration(expiresIn) * time.Second)
}

// bearerHeader builds the Authorization header without ever formatting the
// token into a message.
func bearerHeader(token string) map[string][]string {
	return map[string][]string{"Authorization": {"Bearer " + token}}
}
