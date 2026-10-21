package provider

import "testing"

func TestNormalizeBaseURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://api.example.com/v1":                                         "https://api.example.com/v1",
		"https://api.example.com/v1/":                                        "https://api.example.com/v1",
		"HTTPS://" + "USER" + ":" + "PW" + "@API.Example.com:443/v1//?q=1#f": "https://api.example.com/v1",
		"http://api.example.com:80/v1":                                       "http://api.example.com/v1",
		"https://api.example.com:8443/v1":                                    "https://api.example.com:8443/v1",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	if NormalizeBaseURL("not a url") != "not a url" {
		t.Error("malformed URL must be returned unchanged")
	}
}
