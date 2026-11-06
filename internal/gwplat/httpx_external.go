package gwplat

import (
	"net/http"
	"net/url"
)

// ExternalURL reconstructs the URL a platform signed.
func ExternalURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = v
	}
	u := url.URL{Scheme: scheme, Host: r.Host, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery}
	return u.String()
}
