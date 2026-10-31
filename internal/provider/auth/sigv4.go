package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// SigV4Config is the AWS credential and signing scope. Now is injectable so
// the canonical X-Amz-Date and the signature are deterministic in tests.
type SigV4Config struct {
	AccessKey, SecretKey, SessionToken string
	Region, Service                    string
	Now                                func() time.Time
}

// SigV4 signs requests for AWS Bedrock (SPEC §4.1). Safe for concurrent use.
type SigV4 struct{ cfg SigV4Config }

// NewSigV4 validates the credential; it performs no I/O.
func NewSigV4(c *SigV4Config) (*SigV4, error) {
	if c.AccessKey == "" || c.SecretKey == "" || c.Region == "" || c.Service == "" {
		return nil, fmt.Errorf("%w: sigv4 needs access key, secret key, region and service", ErrCredential)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &SigV4{cfg: *c}, nil
}

// Sign implements [Signer]. payload must be the exact body sent.
func (s *SigV4) Sign(_ context.Context, req *http.Request, payload []byte) error {
	if err := endpointOK(req.URL.String()); err != nil {
		return err
	}
	now := s.cfg.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	req.Header.Set("X-Amz-Date", amzDate)
	if s.cfg.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.cfg.SessionToken)
	}
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	headers, signed := canonicalHeaders(req)
	creq := strings.Join([]string{
		req.Method, canonicalURI(req.URL), canonicalQuery(req.URL),
		headers, signed, payloadHash,
	}, "\n")
	date := now.Format("20060102")
	scope := date + "/" + s.cfg.Region + "/" + s.cfg.Service + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hashHex(creq)
	key := signingKey(s.cfg.SecretKey, date, s.cfg.Region, s.cfg.Service)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.cfg.AccessKey+"/"+scope+
		", SignedHeaders="+signed+", Signature="+hex.EncodeToString(hmacSHA256(key, sts)))
	return nil
}

// signedHeaderNames are the headers SigV4 covers when present.
var signedHeaderNames = []string{ //nolint:gochecknoglobals // immutable signing rule
	"Content-Type", "X-Amz-Date", "X-Amz-Security-Token", "X-Amz-Content-Sha256",
}

func canonicalHeaders(req *http.Request) (block, signed string) {
	host := req.URL.Host
	if req.Host != "" {
		host = req.Host
	}
	v := map[string]string{"host": host}
	for _, name := range signedHeaderNames {
		if vals := req.Header.Values(name); len(vals) > 0 {
			v[strings.ToLower(name)] = strings.Join(vals, ",")
		}
	}
	names := slices.Sorted(maps.Keys(v))
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strings.Join(strings.Fields(v[name]), " "))
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// canonicalURI percent-encodes every path byte AWS does not leave literal,
// preserving existing escapes so an encoded path is never double-encoded.
func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	var b strings.Builder
	for i := range len(p) {
		c := p[i]
		if isUnreserved(c) || c == '/' || c == '%' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func canonicalQuery(u *url.URL) string {
	q := u.Query()
	keys := slices.Sorted(maps.Keys(q))
	var parts []string
	for _, k := range keys {
		vals := q[k]
		slices.Sort(vals)
		for _, v := range vals {
			parts = append(parts, encodeQuery(k)+"="+encodeQuery(v))
		}
	}
	return strings.Join(parts, "&")
}

func encodeQuery(s string) string {
	var b strings.Builder
	for i := range len(s) {
		if c := s[i]; isUnreserved(c) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func signingKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}
