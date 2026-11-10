//go:build !minimal

package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
)

//go:embed pwassets
var pwaFiles embed.FS

// pwaFile serves the root-scoped manifest and service worker (GAP-UX-10).
func (s *Server) pwaFile(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	data, err := fs.ReadFile(pwaFiles, "pwassets/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := "application/json"
	if name == "sw.js" {
		ctype = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=60")
	http.ServeContent(w, r, name, s.now(), bytes.NewReader(data))
}
