package gwpeer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client sends signed peer DMs. It is stateless apart from the injected clock,
// so one client can address every registered peer.
type Client struct {
	LocalID string
	Key     ed25519.PrivateKey
	Tokens  TokenSource
	HTTP    Doer
	Now     func() time.Time
}

// Doer is the HTTP transport seam (E-05): the composition root injects a netx
// client, and gwpeer never constructs one itself.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ErrNoTransport refuses a send without an injected transport.
var ErrNoTransport = errors.New("gwpeer: no HTTP transport")

// Send signs env and posts it to peer.URL. A duplicate answer (the receiver
// already delivered this message id) is returned as ErrDuplicate; a caller
// that retries for idempotency treats it as success.
func (c *Client) Send(ctx context.Context, peer *Peer, chat, text string) (*Envelope, error) {
	if err := peer.Validate(); err != nil {
		return nil, err
	}
	env := &Envelope{
		Version:   ProtocolVersion,
		ID:        NewID(),
		From:      c.LocalID,
		To:        peer.ID,
		Nonce:     NewID(),
		Timestamp: c.now().Unix(),
		Chat:      chat,
		Text:      text,
	}
	if err := env.Sign(c.Key); err != nil {
		return nil, err
	}
	if err := c.post(ctx, peer, env); err != nil {
		return env, err
	}
	return env, nil
}

func (c *Client) post(ctx context.Context, peer *Peer, env *Envelope) error {
	token, err := c.Tokens.Token(ctx, peer.TokenRef)
	if err != nil {
		return fmt.Errorf("gwpeer: token %s: %w", peer.TokenRef, err)
	}
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("gwpeer: encode envelope: %w", err)
	}
	url := strings.TrimRight(peer.URL, "/") + "/peer/v1/dm"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gwpeer: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(peerAuthHeader, c.LocalID)
	if c.HTTP == nil {
		return ErrNoTransport
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("gwpeer: send %s: %w", peer.ID, err)
	}
	reply, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if jerr := errors.Join(rerr, resp.Body.Close()); jerr != nil {
		return fmt.Errorf("gwpeer: read reply: %w", jerr)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrDuplicate, env.ID)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrUnauthorized, peer.ID)
	default:
		return fmt.Errorf("gwpeer: peer %s answered %d: %s", peer.ID, resp.StatusCode, strings.TrimSpace(string(reply)))
	}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// IsDuplicate reports the idempotent-retry outcome of Send.
func IsDuplicate(err error) bool { return errors.Is(err, ErrDuplicate) }

// Fingerprint is the short public-key identifier shown by `damnbot peer
// status`; it is not secret and is stable for one key pair.
func Fingerprint(pub ed25519.PublicKey) string {
	if len(pub) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}
