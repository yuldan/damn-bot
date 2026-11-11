package web

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fsRead(fsys fs.FS, name string) (string, error) {
	raw, err := fs.ReadFile(fsys, name)
	return string(raw), err
}

// TestF8WebResponsive: the web UI is responsive from 360 px with 44 px
// targets, installable (manifest + icons), and the service worker caches only
// static assets (GAP-UX-10).
func TestF8WebResponsive(t *testing.T) {
	t.Parallel()
	srv, ts := newTestServer(t, nil)
	session, _ := signIn(t, srv, ts)
	checkDashboard(t, ts, session)
	checkManifest(t, ts)
	checkServiceWorker(t, ts)
	checkResponsiveCSS(t)
}

func checkDashboard(t *testing.T, ts *httptest.Server, session *http.Cookie) {
	t.Helper()
	page := get(t, ts, "/", session)
	for _, want := range []string{`name="viewport"`, `rel="manifest"`, `href="/manifest.webmanifest"`} {
		if !strings.Contains(page.body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
}

func checkManifest(t *testing.T, ts *httptest.Server) {
	t.Helper()
	man := get(t, ts, "/manifest.webmanifest")
	if man.status != http.StatusOK || man.header.Get("Content-Type") != "application/json" {
		t.Fatalf("manifest = %d %q", man.status, man.header.Get("Content-Type"))
	}
	var body struct {
		Name    string `json:"name"`
		Start   string `json:"start_url"`
		Display string `json:"display"`
		Icons   []struct{ Src, Sizes, Type string }
	}
	if err := json.Unmarshal([]byte(man.body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Name == "" || body.Start != "/" || body.Display != "standalone" || len(body.Icons) < 2 {
		t.Fatalf("manifest = %+v", body)
	}
	for _, icon := range body.Icons {
		if !strings.HasPrefix(icon.Src, "/icon-") || icon.Type != "image/png" {
			t.Errorf("icon = %+v", icon)
		}
		if got := get(t, ts, icon.Src).status; got != http.StatusOK {
			t.Errorf("%s = %d", icon.Src, got)
		}
	}
}

func checkServiceWorker(t *testing.T, ts *httptest.Server) {
	t.Helper()
	sw := get(t, ts, "/sw.js")
	if sw.status != http.StatusOK || !strings.HasPrefix(sw.header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("sw = %d %q", sw.status, sw.header.Get("Content-Type"))
	}
	for _, bad := range []string{`"/api/`, `"/auth/`, "localStorage", "sessionStorage"} {
		if strings.Contains(sw.body, bad) {
			t.Errorf("service worker caches or stores %q", bad)
		}
	}
	if !strings.Contains(sw.body, `"/assets/app.css"`) || !strings.Contains(sw.body, "caches.match") {
		t.Error("service worker is not cache-only for static assets")
	}
}

func checkResponsiveCSS(t *testing.T) {
	t.Helper()
	assets, err := defaultAssets()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fsRead(assets, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"max-width: 400px", "min-height: 44px", "min-width: 44px"} {
		if !strings.Contains(raw, want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	js, err := fsRead(assets, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js, `serviceWorker.register("/sw.js")`) {
		t.Error("dashboard does not register the service worker")
	}
}
