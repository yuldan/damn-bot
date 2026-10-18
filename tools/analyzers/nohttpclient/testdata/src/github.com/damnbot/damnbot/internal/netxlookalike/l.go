// Package netxlookalike shares a prefix with netx but is not netx.
package netxlookalike

import "net/http"

var _ = &http.Client{} // want `composite literal of http.Client`
