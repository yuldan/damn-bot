// Package good holds negative cases: none of these constructs violate E-05.
package good

import (
	"context"
	"net"
	"net/http"
	"time"
)

// A client received from netx may be used freely, including its methods
// whose names collide with forbidden package-level funcs.
func methods(ctx context.Context, c *http.Client, d *net.Dialer, u string) {
	_, _ = c.Get(u)
	_, _ = c.Head(u)
	_, _ = c.Post(u, "text/plain", nil)
	_, _ = d.Dial("tcp", "x:1")
	_, _ = d.DialContext(ctx, "tcp", "x:1")
}

// Type references without construction are allowed.
func types(c *http.Client, t *http.Transport) (*http.Client, http.RoundTripper) {
	var _ *net.Dialer
	return c, t
}

// Other net/http values and server-side APIs are allowed.
func other() {
	_ = http.Header{}
	_ = &http.Request{}
	_ = http.Server{ReadHeaderTimeout: time.Second}
	_, _ = http.NewRequest(http.MethodGet, "http://x", nil)
	_, _ = net.Listen("tcp", "127.0.0.1:0")
	_ = net.ParseIP("127.0.0.1")
	_ = new(http.Request)
}

// Local identifiers shadowing forbidden names are not the net/http objects.
type Client struct{}

func Get(string) {}

func shadow() {
	DefaultClient := 1
	_ = DefaultClient
	_ = Client{}
	_ = &Client{}
	Get("x")
}
