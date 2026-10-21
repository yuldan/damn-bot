package provider

import (
	"net/url"
	"strings"
)

// NormalizeBaseURL is the route identity used as the carrier family.
func NormalizeBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = u.Hostname()
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return strings.TrimRight(u.String(), "/")
}
