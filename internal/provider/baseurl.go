package provider

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// redactBaseURL removes userinfo, query, and fragment from an error message:
// credentials must never reach stderr. An unparsable value is not echoed.
func redactBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid>"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

// NormalizeBaseURL is the route identity used as the carrier family. It
// refuses an unparsable URL instead of returning credentials verbatim.
func NormalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("provider: invalid base_url %q", redactBaseURL(raw))
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") { // IPv6 literal keeps its brackets
		host = "[" + host + "]"
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
}
