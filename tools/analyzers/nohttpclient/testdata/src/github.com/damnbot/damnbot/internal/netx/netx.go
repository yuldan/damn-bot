// Package netx is the single owner of HTTP clients and dialers (E-05).
package netx

import (
	"net"
	"net/http"
	"time"
)

func New() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: time.Second}).DialContext
	return &http.Client{Transport: t}
}
