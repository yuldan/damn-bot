//go:build minimal

package web

import "net/http"

// pwaFile is absent from the minimal build: the PWA assets are not embedded,
// so the manifest and service worker answer 404 while the UI still works.
func (s *Server) pwaFile(w http.ResponseWriter, r *http.Request, _ string) {
	http.NotFound(w, r)
}
