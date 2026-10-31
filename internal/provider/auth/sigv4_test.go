package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// clockAt returns a fixed clock for deterministic signatures.
func clockAt(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestSigV4AWSVector signs the AWS SigV4 header-auth example whose canonical
// request hash is published as 816cd5b4…; the signature below is that hash
// through the documented string-to-sign and key derivation.
func TestSigV4AWSVector(t *testing.T) {
	t.Parallel()
	s, err := NewSigV4(&SigV4Config{ //nolint:gosec // published AWS SigV4 test vector
		AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		Region: "us-east-1", Service: "service",
		Now: clockAt(time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"https://example.amazonaws.com/?Param2=value2&Param1=value1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Sign(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	const want = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-date, " +
		"Signature=b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization =\n%s\nwant\n%s", got, want)
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
		t.Fatalf("X-Amz-Date = %q", got)
	}
}

// TestSigV4SessionTokenAndPayloadHash covers the Bedrock shape: a temporary
// session token is signed and sent, and the payload hash is the body hash.
func TestSigV4SessionTokenAndPayloadHash(t *testing.T) {
	t.Parallel()
	s, err := NewSigV4(&SigV4Config{
		AccessKey: "AKID", SecretKey: "secret", SessionToken: "session",
		Region: "us-east-1", Service: "bedrock",
		Now: clockAt(time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://bedrock-runtime.us-east-1.amazonaws.com/model/anthropic.claude:1/invoke", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := s.Sign(context.Background(), req, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-Amz-Security-Token"); got != "session" {
		t.Fatalf("session token = %q", got)
	}
	authz := req.Header.Get("Authorization")
	if !strings.Contains(authz, "SignedHeaders=content-type;host;x-amz-date;x-amz-security-token") {
		t.Fatalf("signed headers = %q", authz)
	}
	// A different payload must change the signature.
	before := authz
	if err := s.Sign(context.Background(), req, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") == before {
		t.Fatal("signature did not cover the payload")
	}
}

// TestSigV4RejectsBadInput is the negative control: no credential, no TLS
// (except loopback), and no silent unsigned request.
func TestSigV4RejectsBadInput(t *testing.T) {
	t.Parallel()
	if _, err := NewSigV4(&SigV4Config{Region: "r", Service: "s"}); !errors.Is(err, ErrCredential) {
		t.Fatalf("missing key err = %v", err)
	}
	s, err := NewSigV4(&SigV4Config{AccessKey: "a", SecretKey: "b", Region: "r", Service: "s"})
	if err != nil {
		t.Fatal(err)
	}
	req, rerr := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.amazonaws.com/", nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if serr := s.Sign(context.Background(), req, nil); !errors.Is(serr, ErrEndpoint) {
		t.Fatalf("http endpoint err = %v", serr)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("credential was added to a rejected endpoint")
	}
	local := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer local.Close()
	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, local.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Sign(context.Background(), req, nil); err != nil {
		t.Fatalf("loopback endpoint: %v", err)
	}
}
