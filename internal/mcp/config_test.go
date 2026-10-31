package mcp

import (
	"errors"
	"testing"
	"time"
)

func TestServerNormalizeAndValidate(t *testing.T) {
	t.Parallel()
	s := Server{Name: "github", Command: []string{"github-mcp-server", "stdio"}, Env: map[string]string{"TOKEN": "keychain:gh"}}
	s.Normalize()
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.Trust != TrustUntrusted || s.Timeout() != 120*time.Second {
		t.Fatalf("defaults = %q/%v", s.Trust, s.Timeout())
	}
	full := Server{Name: "remote", URL: "https://h.test/mcp?x=1", Headers: map[string]string{"Authorization": "keychain:r"}, Trust: TrustFull, TimeoutS: 30}
	full.Normalize()
	if err := full.Validate(); err != nil {
		t.Fatalf("http Validate: %v", err)
	}
}

func TestServerRejects(t *testing.T) {
	t.Parallel()
	cases := []Server{
		{Name: "bad name", Command: []string{"x"}},
		{Name: "s"},
		{Name: "s", Command: []string{"x"}, URL: "https://h/mcp"},
		{Name: "s", Command: []string{""}},
		{Name: "s", Command: []string{"x"}, Trust: "yes"},
		{Name: "s", Command: []string{"x"}, TimeoutS: 601},
		{Name: "s", Command: []string{"x"}, Env: map[string]string{"1BAD": "v"}},
		{Name: "s", URL: "https://u:p@h/mcp"}, //nolint:gosec // fixture for the rejected userinfo URL
		{Name: "s", URL: "ftp://h/mcp"},
		{Name: "s", URL: "https://h/mcp", Env: map[string]string{"A": "b"}},
	}
	for i := range cases {
		s := cases[i]
		s.Normalize()
		if err := s.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("case %d Validate = %v, want ErrInvalidConfig", i, err)
		}
	}
}
