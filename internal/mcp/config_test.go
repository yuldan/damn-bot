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
		{Name: "s", URL: "https://h/mcp", Headers: map[string]string{"Authorization": "Bearer x"}},
		{Name: "s", URL: "https://h/mcp", Headers: map[string]string{"Mcp-Session-Id": "keychain:x"}},
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

func TestConfigSHADetectsChange(t *testing.T) {
	t.Parallel()
	base := Server{Name: "s", Command: []string{"x"}, TimeoutS: 10, Trust: TrustUntrusted}
	a, err := base.ConfigSHA()
	if err != nil {
		t.Fatal(err)
	}
	b, err := base.ConfigSHA()
	if err != nil || a != b {
		t.Fatalf("sha not deterministic: %q/%q, %v", a, b, err)
	}
	changed := base
	changed.TimeoutS = 11
	c, err := changed.ConfigSHA()
	if err != nil || c == a {
		t.Fatalf("changed sha = %q, want different from %q", c, a)
	}
}

func TestNewClientRequiresHostAndHTTPDoer(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(nil, Server{Name: "s", Command: []string{"x"}}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil host = %v", err)
	}
	h := &fakeHost{store: newStore()}
	if _, err := NewClient(h, Server{Name: "s", URL: "https://h/mcp"}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil doer = %v", err)
	}
}
