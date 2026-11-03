//go:build minimal

package netx

import (
	"bytes"
	"net/http"
)

// appendScannedHeaders scans every header: the minimal build has no Tool
// backend that installs its own credential header.
func appendScannedHeaders(buf *bytes.Buffer, req *http.Request) {
	for _, h := range []http.Header{req.Header, req.Trailer} {
		for name, vals := range h {
			for _, v := range vals {
				buf.WriteString(name + ": " + v + "\n")
			}
		}
	}
}
