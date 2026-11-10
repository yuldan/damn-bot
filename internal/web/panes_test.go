package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakePanes is the in-memory PaneBackend of the package tests.
type fakePanes struct {
	open   []Pane
	closed bool
}

func (f *fakePanes) List(context.Context) ([]Pane, error) { return f.open, nil }

func (f *fakePanes) Open(_ context.Context, req PaneOpen) (Pane, error) {
	if req.Kind != PanePreview {
		return Pane{}, ErrPaneMissing
	}
	p := Pane{ID: "p1", Kind: PanePreview, Title: req.Target, Created: time.Now()}
	f.open = append(f.open, p)
	return p, nil
}

func (f *fakePanes) Input(context.Context, string, []byte) error { return nil }

func (f *fakePanes) Output(_ context.Context, id string, offset int) (PaneChunk, error) {
	if id != "p1" {
		return PaneChunk{}, ErrPaneMissing
	}
	data := []byte("hello")
	if offset > len(data) {
		offset = len(data)
	}
	return PaneChunk{Data: data[offset:], Next: len(data), Open: false}, nil
}

func (f *fakePanes) Close(_ context.Context, id string) error {
	if id != "p1" || f.closed {
		return ErrPaneMissing
	}
	f.closed = true
	f.open = nil
	return nil
}

// fakeProjects is the in-memory ProjectSource.
type fakeProjects struct{}

func (fakeProjects) Projects(context.Context) ([]Project, error) {
	return []Project{{Name: "demo", Primary: "/work/demo", Folders: []string{"/work/demo"}, Current: true}}, nil
}

// TestWebPanes covers the authenticated pane and project API (UI-017).
func TestWebPanes(t *testing.T) {
	srv, ts := newTestServer(t, func(c *Config) {
		c.Panes = &fakePanes{}
		c.Projects = fakeProjects{}
	})
	if got := fetch(t, ts, http.MethodGet, "/api/panes", ""); got.status != http.StatusUnauthorized {
		t.Fatalf("anonymous panes = %d, want 401", got.status)
	}
	if got := fetch(t, ts, http.MethodGet, "/api/projects", ""); got.status != http.StatusUnauthorized {
		t.Fatalf("anonymous projects = %d, want 401", got.status)
	}
	session, csrf := signIn(t, srv, ts)

	projects := get(t, ts, "/api/projects", session)
	if projects.status != http.StatusOK || !strings.Contains(projects.body, `"name":"demo"`) {
		t.Fatalf("projects = %d %q", projects.status, projects.body)
	}
	open, body := panePost(t, ts, "/api/panes/open", `{"kind":"preview","target":"README.md"}`, session, csrf)
	if open != http.StatusOK || !strings.Contains(body, `"id":"p1"`) {
		t.Fatalf("open = %d %q", open, body)
	}
	out := get(t, ts, "/api/panes/output?id=p1&offset=0", session)
	var chunk PaneChunk
	if out.status != http.StatusOK || json.Unmarshal([]byte(out.body), &chunk) != nil || string(chunk.Data) != "hello" {
		t.Fatalf("output = %d %q", out.status, out.body)
	}
	if status, _ := panePost(t, ts, "/api/panes/open", `{"kind":"other"}`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d, want 400", status)
	}
	if got := get(t, ts, "/api/panes/output?id=missing", session); got.status != http.StatusNotFound {
		t.Fatalf("missing pane = %d, want 404", got.status)
	}
	if status, _ := panePost(t, ts, "/api/panes/close", `{"id":"p1"}`, session, csrf); status != http.StatusNoContent {
		t.Fatalf("close = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/close", `{"id":"p1"}`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("double close = %d, want 400", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/close", `{"id":"p1"}`, session, nil); status != http.StatusForbidden {
		t.Fatalf("close without csrf = %d, want 403", status)
	}
}

// panePost sends one CSRF-signed pane request and returns status and body.
func panePost(t *testing.T, ts *httptest.Server, path, payload string, session, csrf *http.Cookie) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+path, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(session)
	if csrf != nil {
		req.AddCookie(csrf)
		req.Header.Set(csrfHeader, csrf.Value)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, derr := ts.Client().Do(req)
	if derr != nil {
		t.Fatal(derr)
	}
	raw, rerr := io.ReadAll(resp.Body)
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	if rerr != nil {
		t.Fatal(rerr)
	}
	return resp.StatusCode, string(raw)
}
