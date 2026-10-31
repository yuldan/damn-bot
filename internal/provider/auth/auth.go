// Package auth implements the provider transport credential layer of
// SPEC §4.1: AWS SigV4 (Bedrock), Google service-account JWT (Vertex), and
// OAuth 2.0 device/PKCE/DCR with a token store in the OS keychain or a 0600
// file. Every signer receives the exact bytes that go on the wire, so a
// signature can never cover a different payload. No value handled here is
// ever logged or included in an error, and every endpoint must be HTTPS or
// loopback.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Errors are sentinels so callers classify with errors.Is, never by text.
var (
	// ErrCredential marks a missing or unusable credential.
	ErrCredential = errors.New("auth: credential")
	// ErrEndpoint marks a non-HTTPS (and non-loopback) endpoint.
	ErrEndpoint = errors.New("auth: endpoint must be https")
	// ErrOffline reports a flow that needs the network in offline mode.
	ErrOffline = errors.New("auth: offline mode")
	// ErrTokenMissing reports that no stored token exists for a provider.
	ErrTokenMissing = errors.New("auth: token not found")
	// ErrInvalidGrant is RFC 6749 §5.2 invalid_grant (revoked or expired).
	ErrInvalidGrant = errors.New("auth: invalid grant")
	// ErrUnsupported reports a flow the authorization server does not offer.
	ErrUnsupported = errors.New("auth: unsupported by server")
)

// Signer adds authentication to one outbound inference request. Sign is
// called once per attempt and must be safe for concurrent use.
type Signer interface {
	Sign(ctx context.Context, req *http.Request, payload []byte) error
}

// Bearer is a static bearer token, the openai-chat default.
type Bearer string

// Sign implements [Signer].
func (b Bearer) Sign(_ context.Context, req *http.Request, _ []byte) error {
	if b == "" {
		return fmt.Errorf("%w: empty bearer token", ErrCredential)
	}
	req.Header.Set("Authorization", "Bearer "+string(b))
	return nil
}

// endpointOK enforces the transport rule: a credential may only travel over
// TLS, or to loopback for a local test/authorization server.
func endpointOK(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEndpoint, err)
	}
	if u.Scheme == "https" && u.Host != "" {
		return nil
	}
	if u.Scheme == "http" && isLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrEndpoint, u.Redacted())
}

// isLoopback reports whether host is a loopback literal or "localhost".
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
