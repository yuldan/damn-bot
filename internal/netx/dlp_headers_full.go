//go:build !minimal

package netx

import (
	"bytes"
	"net/http"
)

// appendScannedHeaders scans every header except the runtime-installed
// credential headers explicitly exempted for this request (GAP-SEC-16).
func appendScannedHeaders(buf *bytes.Buffer, req *http.Request) {
	exempt := authHeaders(req.Context())
	for _, h := range []http.Header{req.Header, req.Trailer} {
		for name, vals := range h {
			if _, ok := exempt[http.CanonicalHeaderKey(name)]; ok {
				continue
			}
			for _, v := range vals {
				buf.WriteString(name + ": " + v + "\n")
			}
		}
	}
}
