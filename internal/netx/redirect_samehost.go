//go:build !minimal

package netx

import "net/http"

// WithSameHostRedirects returns a shallow copy whose redirect policy follows at
// most max redirects and never changes host. It exists for catalog fetches
// (GAP-SEC-20): a manifest must come from the host the operator configured, so
// a cross-host Location is returned as a 3xx instead of followed.
func (c *Client) WithSameHostRedirects(max int) *Client {
	clone := *c
	hc := *c.hc
	clone.hc = &hc
	clone.hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > max || req.URL.Host != via[0].URL.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &clone
}
