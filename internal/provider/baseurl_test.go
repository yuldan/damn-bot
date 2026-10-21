package provider

import "testing"

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
}
