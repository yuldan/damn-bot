package provider

import (
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	t.Parallel()
	ok := map[string]string{
		"https://api.example.com/v1":                                         "https://api.example.com/v1",
		"https://api.example.com/v1/":                                        "https://api.example.com/v1",
		"HTTPS://" + "USER" + ":" + "PW" + "@API.Example.com:443/v1//?q=1#f": "https://api.example.com/v1",
		"http://api.example.com:80/v1":                                       "http://api.example.com/v1",
		"https://api.example.com:8443/v1":                                    "https://api.example.com:8443/v1",
		"https://[::1]:443/v1/":                                              "https://[::1]/v1",
		"https://[::1]:8443/v1":                                              "https://[::1]:8443/v1",
	}
	for in, want := range ok {
		if got, err := NormalizeBaseURL(in); err != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"not a url", "https://", "api.example.com/v1"} {
		if got, err := NormalizeBaseURL(bad); err == nil {
			t.Errorf("NormalizeBaseURL(%q) = %q, want error", bad, got)
		}
	}
	// An error must never echo credentials or query secrets.
	if _, err := NormalizeBaseURL("https://" + "user" + ":" + "pw" + "@"); err == nil ||
		strings.Contains(err.Error(), "pw") {
		t.Fatalf("error leaks credentials: %v", err)
	}
	if _, err := NormalizeBaseURL("https:///v1?token=SECRET"); err == nil ||
		strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error leaks query: %v", err)
	}
	for _, raw := range []string{"user:pw@host/v1", "https:user:pw@host", "sk-bare-key"} {
		_, err := NormalizeBaseURL(raw)
		if err == nil || strings.Contains(err.Error(), "pw") || strings.Contains(err.Error(), "sk-bare") {
			t.Fatalf("scheme-less %q leaks: %v", raw, err)
		}
	}
}

// TestAnthropicFamily: the cache_control gate reads the vendor from the
// normalized base URL's host, so the production family of an Anthropic origin
// matches while every other origin — and anything unparsable — does not.
func TestAnthropicFamily(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"anthropic",
		"https://api.anthropic.com/v1",
		"https://API.Anthropic.COM:8443/v1",
		"https://foo.anthropic.com",
	} {
		if !anthropicFamily(in) {
			t.Errorf("anthropicFamily(%q) = false, want true", in)
		}
	}
	for _, in := range []string{
		"", "openai", "https://anthropic.com", "https://evilanthropic.com",
		"https://api.openai.com/v1", "http://127.0.0.1:8080/v1",
		"https://api.anthropic.com.evil.test/v1", "http://[::1", "anthropic.com/v1",
	} {
		if anthropicFamily(in) {
			t.Errorf("anthropicFamily(%q) = true, want false", in)
		}
	}
}
