package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// OAuthError is a failed token or registration response, reduced to the RFC
// 6749 code and status: the body may echo a credential.
type OAuthError struct {
	Code   string
	Status int
}

// Error implements error.
func (e *OAuthError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("auth: token endpoint: status %d", e.Status)
	}
	return fmt.Sprintf("auth: token endpoint: %s (status %d)", e.Code, e.Status)
}

// Is maps invalid_grant to [ErrInvalidGrant].
func (e *OAuthError) Is(target error) bool {
	return target == ErrInvalidGrant && e.Code == "invalid_grant"
}

// tokenResponse is the shared RFC 6749 §5.1/§5.2 body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

const maxTokenBody = 64 << 10

func doForm(ctx context.Context, c *netx.Client, endpoint string, form url.Values) (tokenResponse, error) {
	raw, err := doJSONRaw(ctx, c, http.MethodPost, endpoint, form.Encode(), "application/x-www-form-urlencoded")
	if err != nil {
		return tokenResponse{}, err
	}
	var out tokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return tokenResponse{}, fmt.Errorf("auth: decode token response: %w", err)
	}
	return out, nil
}

func doJSONRaw(ctx context.Context, c *netx.Client, method, endpoint, body, ctype string) ([]byte, error) {
	if err := endpointOK(endpoint); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("auth: request: %w", err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set("Accept", "application/json")
	return doNetx(c, req)
}

func doNetx(c *netx.Client, req *http.Request) ([]byte, error) {
	resp, err := c.DoOK(req)
	if err != nil {
		if se, ok := errors.AsType[*netx.StatusError](err); ok {
			return nil, oauthStatusError(se.StatusCode, se.Body)
		}
		return nil, fmt.Errorf("auth: endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // body already fully read/limited
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return nil, fmt.Errorf("auth: read response: %w", err)
	}
	return raw, nil
}

// oauthStatusError reduces a non-2xx body to its OAuth error code.
func oauthStatusError(status int, body []byte) error {
	var out struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &out) == nil && out.Error != "" {
		return &OAuthError{Code: out.Error, Status: status}
	}
	return &OAuthError{Status: status}
}
