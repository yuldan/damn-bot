package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func TestOSVCheck(t *testing.T) {
	t.Parallel()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/query" {
			http.NotFound(w, r)
			return
		}
		decodeJSON(t, r.Body, &body)
		writeBody(t, w, `{"vulns":[{"id":"GHSA-1","summary":"one"},{"id":"GHSA-1","summary":"one"},{"id":"GHSA-2","summary":"two"}]}`)
	}))
	defer srv.Close()
	raw, merr := json.Marshal(map[string]any{"enabled": true, "base_url": srv.URL})
	if merr != nil {
		t.Fatalf("raw: %v", merr)
	}
	cfg := Config{Enabled: true, Timeout: 5 * time.Second, OSV: raw}
	m := New(&cfg, Deps{Now: time.Now})
	if serr := m.Start(t.Context()); serr != nil {
		t.Fatalf("start: %v", serr)
	}
	res, err := m.Invoke(t.Context(), "osv_check", json.RawMessage(`{"package":"golang.org/x/text","version":"0.3.0","ecosystem":"Go"}`), host.Env{})
	if err != nil {
		t.Fatalf("osv: %v", err)
	}
	var out struct {
		Vulns []map[string]string `json:"vulns"`
		Count int                 `json:"count"`
	}
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil || out.Count != 2 {
		t.Fatalf("result = %q err %v", res.Content, err)
	}
	if res.UntrustedSource != "connector:osv" {
		t.Fatalf("untrusted = %q", res.UntrustedSource)
	}
	data := mustJSON(t, body)
	if !strings.Contains(data, `"ecosystem":"Go"`) || !strings.Contains(data, `"version":"0.3.0"`) {
		t.Fatalf("query body = %v", body)
	}
}
