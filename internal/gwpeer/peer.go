package gwpeer

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ProtocolVersion is the only peer wire version this package accepts. A
// different version is rejected before the signature is checked.
const ProtocolVersion = 1

// MaxRelayHops is the relay depth ceiling (GW-015): a DM may cross at most two
// relay profiles, so a loop cannot keep a message alive indefinitely.
const MaxRelayHops = 2

// maxTextBytes bounds one DM body. Peer DMs are small control messages, never
// attachment carriers.
const maxTextBytes = 64 << 10

// Identity errors. Every error is stable for the CLI and tests.
var (
	ErrInvalidEnvelope = errors.New("gwpeer: invalid envelope")
	ErrSignature       = errors.New("gwpeer: invalid signature")
	ErrExpired         = errors.New("gwpeer: envelope outside the replay window")
	ErrReplay          = errors.New("gwpeer: nonce replayed")
	ErrDuplicate       = errors.New("gwpeer: duplicate message id")
	ErrUnauthorized    = errors.New("gwpeer: unauthorized peer")
	ErrUnknownPeer     = errors.New("gwpeer: unknown peer")
	ErrLoop            = errors.New("gwpeer: relay loop")
	ErrProfileDenied   = errors.New("gwpeer: relay profile not allowed")
)

// Envelope is one signed peer DM. Signature covers every other field, so a
// relay can bump Hop only by re-signing as the new sender.
type Envelope struct {
	Version   int    `json:"v"`
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"ts"`
	Chat      string `json:"chat"`
	Text      string `json:"text"`
	Hop       int    `json:"hop"`
	Signature []byte `json:"sig,omitempty"`
}

// payload returns the canonical bytes a signature covers: the envelope with an
// empty signature, encoded by encoding/json (field order is struct order).
func (e *Envelope) payload() ([]byte, error) {
	clone := *e
	clone.Signature = nil
	b, err := json.Marshal(&clone)
	if err != nil {
		return nil, fmt.Errorf("gwpeer: encode envelope: %w", err)
	}
	return b, nil
}

// Sign attaches an Ed25519 signature with the local private key.
func (e *Envelope) Sign(priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("%w: private key length %d", ErrSignature, len(priv))
	}
	body, err := e.payload()
	if err != nil {
		return err
	}
	e.Signature = ed25519.Sign(priv, body)
	return nil
}

// Verify checks the signature against the sender's advertised public key.
func (e *Envelope) Verify(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key length %d", ErrSignature, len(pub))
	}
	body, err := e.payload()
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, body, e.Signature) {
		return ErrSignature
	}
	return nil
}

// Validate rejects a structurally invalid or stale envelope. It does not check
// the signature, nonce, or message id; the server does those in order.
func (e *Envelope) Validate(now time.Time, window time.Duration) error {
	if e.Version != ProtocolVersion {
		return fmt.Errorf("%w: version %d", ErrInvalidEnvelope, e.Version)
	}
	for _, f := range [...]struct{ name, value string }{
		{"id", e.ID}, {"from", e.From}, {"to", e.To}, {"nonce", e.Nonce}, {"chat", e.Chat},
	} {
		if f.value == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidEnvelope, f.name)
		}
		if len(f.value) > 512 || strings.ContainsAny(f.value, "\x00\r\n") {
			return fmt.Errorf("%w: %s is malformed", ErrInvalidEnvelope, f.name)
		}
	}
	if len(e.Text) > maxTextBytes {
		return fmt.Errorf("%w: text is too large", ErrInvalidEnvelope)
	}
	if e.Hop < 0 || e.Hop > MaxRelayHops {
		return fmt.Errorf("%w: hop %d", ErrInvalidEnvelope, e.Hop)
	}
	ts := time.Unix(e.Timestamp, 0)
	if window <= 0 {
		window = DefaultReplayWindow
	}
	if ts.Before(now.Add(-window)) || ts.After(now.Add(window)) {
		return fmt.Errorf("%w: timestamp %s", ErrExpired, ts.UTC().Format(time.RFC3339))
	}
	return nil
}

// NewID returns a random 128-bit hex identifier for a message or nonce.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; a panic here would
		// take the gateway down, so fall back to a time-derived value.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Peer book errors.
var (
	ErrInvalidPeer  = errors.New("gwpeer: invalid peer")
	ErrPeerExists   = errors.New("gwpeer: peer already registered")
	ErrPeerNotFound = errors.New("gwpeer: peer not registered")
)

// Peer is one registered remote gateway. TokenRef names the secret source of
// the bearer token; the token itself is never written to the book.
type Peer struct {
	ID        string            `json:"id"`
	URL       string            `json:"url"`
	TokenRef  string            `json:"token_ref"`
	PublicKey ed25519.PublicKey `json:"public_key"`
	AddedAt   time.Time         `json:"added_at"`
}

// Validate rejects a peer that could steer the client outside the reviewed
// surface: plaintext HTTP is allowed only for loopback, and credentials in the
// URL are never accepted.
func (p *Peer) Validate() error {
	for _, f := range [...]struct{ name, value string }{
		{"id", p.ID}, {"url", p.URL}, {"token_ref", p.TokenRef},
	} {
		if f.value == "" || len(f.value) > 512 || strings.ContainsAny(f.value, "\x00\r\n") {
			return fmt.Errorf("%w: %s is required or malformed", ErrInvalidPeer, f.name)
		}
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return fmt.Errorf("%w: url: %w", ErrInvalidPeer, err)
	}
	if err := validatePeerURL(u); err != nil {
		return err
	}
	if len(p.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key length %d", ErrInvalidPeer, len(p.PublicKey))
	}
	return nil
}

// validatePeerURL rejects a URL that could redirect the client outside the
// reviewed surface.
func validatePeerURL(u *url.URL) error {
	if u.User != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: url must be scheme://host[:port]", ErrInvalidPeer)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopbackHost(u.Hostname()) {
			return fmt.Errorf("%w: plaintext http is loopback only", ErrInvalidPeer)
		}
	default:
		return fmt.Errorf("%w: unsupported scheme %q", ErrInvalidPeer, u.Scheme)
	}
	return nil
}

// loopbackHost reports whether host is a loopback name or address.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
