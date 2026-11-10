//go:build !minimal

package desktopui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

type fakeSurface struct {
	panes     []string
	active    string
	calls     []string
	annotate  [2]string
	preview   string
	drive     Drive
	focus     string
	tour      string
	react     [2]string
	closePane string
	termOut   string
	belowOut  string
	theme     string
	tip       string
	setup     int
	project   string
	err       error
}

func (f *fakeSurface) ActivePane(context.Context) (string, error) {
	if f.active == "" {
		return "", ErrNoActivePane
	}
	return f.active, nil
}
func (f *fakeSurface) Panes(context.Context) ([]string, error) { return f.panes, nil }
func (f *fakeSurface) OpenPreview(_ context.Context, target string) error {
	f.calls, f.preview = append(f.calls, "preview"), target
	return f.err
}

func (f *fakeSurface) Annotate(_ context.Context, pane, selector, note string) error {
	f.calls, f.annotate = append(f.calls, "annotate"), [2]string{pane, selector + "|" + note}
	return f.err
}

func (f *fakeSurface) CloseTerminal(_ context.Context, pane string) error {
	f.calls, f.closePane = append(f.calls, "close"), pane
	return f.err
}

func (f *fakeSurface) Drive(_ context.Context, d Drive) error {
	f.calls, f.drive = append(f.calls, "drive"), d
	return f.err
}

func (f *fakeSurface) FocusPane(_ context.Context, pane string) error {
	f.calls, f.focus = append(f.calls, "focus"), pane
	return f.err
}

func (f *fakeSurface) Tour(_ context.Context, name string) error {
	f.calls, f.tour = append(f.calls, "tour"), name
	return f.err
}

func (f *fakeSurface) React(_ context.Context, id, emoji string) error {
	f.calls, f.react = append(f.calls, "react"), [2]string{id, emoji}
	return f.err
}

func (f *fakeSurface) ReadTerminal(_ context.Context, pane string, max int) (string, error) {
	f.calls = append(f.calls, "read-"+pane)
	return f.termOut, f.err
}

func (f *fakeSurface) ReadBelow(context.Context, int) (string, error) {
	f.calls = append(f.calls, "below")
	return f.belowOut, f.err
}

func (f *fakeSurface) SetTheme(_ context.Context, theme string) error {
	f.calls, f.theme = append(f.calls, "theme"), theme
	return f.err
}

func (f *fakeSurface) ShowTip(_ context.Context, tip string) error {
	f.calls, f.tip = append(f.calls, "tip"), tip
	return f.err
}

func (f *fakeSurface) SetupBeat(context.Context) error {
	f.calls, f.setup = append(f.calls, "setup"), f.setup+1
	return f.err
}

func (f *fakeSurface) Project(_ context.Context, name string) error {
	f.calls, f.project = append(f.calls, "project"), name
	return f.err
}

// previewJSON builds the JSON argument so a Windows path's backslashes are
// escaped instead of breaking the literal.
func previewJSON(t *testing.T, target string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"target": target})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sameFile compares resolved files so a Windows 8.3 short temp path and the
// long path returned by the sandbox are treated as one target.
func sameFile(t *testing.T, got, want string) bool {
	t.Helper()
	gi, err := os.Stat(got)
	if err != nil {
		return false
	}
	wi, err := os.Stat(want)
	if err != nil {
		return false
	}
	return os.SameFile(gi, wi)
}

func guiEnv(t *testing.T, origin, cwd string) host.Env {
	t.Helper()
	v, err := host.NewSessionView("s1", "", origin, cwd, nil, nil)
	if err != nil {
		t.Fatalf("session view: %v", err)
	}
	return host.Env{Session: v, Cwd: cwd}
}

func invoke(t *testing.T, m *Module, env host.Env, tool, args string) (host.Result, error) {
	t.Helper()
	return m.Invoke(context.Background(), tool, json.RawMessage(args), env)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var he *host.Error
	if !errors.As(err, &he) || he.Code != code {
		t.Fatalf("err = %v, want host.Error code %q", err, code)
	}
}

func TestF8TOOL027AnnotatePreview(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{active: "preview", panes: []string{"preview", "terminal"}}
	m := New(s)
	env := guiEnv(t, OriginDesktop, t.TempDir())
	if _, err := invoke(t, m, env, "annotate_preview", `{"selector":"#main","note":"fix this"}`); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	if s.annotate != [2]string{"preview", "#main|fix this"} {
		t.Fatalf("surface annotate = %v", s.annotate)
	}
	// Target must be the active pane: a surface with no active pane refuses.
	empty := New(&fakeSurface{err: ErrNoActivePane})
	_, err := invoke(t, empty, env, "annotate_preview", `{"selector":"#main","note":"x"}`)
	wantCode(t, err, "not_found")
	if _, err := invoke(t, m, guiEnv(t, "run", ""), "annotate_preview", `{"selector":"#x","note":"y"}`); err == nil {
		t.Fatal("annotate outside a GUI session succeeded")
	}
}

func TestF8TOOL028CloseTerminal(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{panes: []string{"terminal-1", "preview"}}
	m := New(s)
	env := guiEnv(t, OriginWeb, t.TempDir())
	foreign := env
	foreign.Approved = true
	_, err := invoke(t, m, foreign, "close_terminal", `{"pane":"terminal-9"}`)
	wantCode(t, err, "not_found")
	if len(s.calls) != 0 {
		t.Fatalf("foreign pane reached the surface: %v", s.calls)
	}
	_, err = invoke(t, m, env, "close_terminal", `{"pane":"terminal-1"}`)
	wantCode(t, err, "denied")
	env.Approved = true
	if _, err := invoke(t, m, env, "close_terminal", `{"pane":"terminal-1"}`); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s.closePane != "terminal-1" {
		t.Fatalf("closed pane = %q", s.closePane)
	}
}

func TestF8TOOL029DesktopPreview(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "report.html")
	if err := os.WriteFile(file, []byte("<html></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.html")
	if err := os.WriteFile(outside, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &fakeSurface{}
	m := New(s)
	env := guiEnv(t, OriginDesktop, root)
	if _, err := invoke(t, m, env, "desktop_preview", previewJSON(t, "report.html")); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !sameFile(t, s.preview, file) {
		t.Fatalf("preview target = %q, want %q", s.preview, file)
	}
	_, err := invoke(t, m, env, "desktop_preview", previewJSON(t, outside))
	wantCode(t, err, "outside_workspace")
	_, err = invoke(t, m, env, "desktop_preview", previewJSON(t, "http://example.com/x"))
	wantCode(t, err, "invalid_args")
	if _, err := invoke(t, m, env, "desktop_preview", previewJSON(t, "https://example.com/x")); err != nil {
		t.Fatalf("https preview: %v", err)
	}
}

func TestF8TOOL030DrivePreview(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{}
	m := New(s)
	env := guiEnv(t, OriginWeb, t.TempDir())
	if _, err := invoke(t, m, env, "drive_preview", `{"action":"snapshot"}`); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	_, err := invoke(t, m, env, "drive_preview", `{"action":"click","selector":"#go"}`)
	wantCode(t, err, "denied")
	if len(s.calls) != 1 {
		t.Fatalf("unapproved click reached the surface: %v", s.calls)
	}
	env.Approved = true
	if _, err := invoke(t, m, env, "drive_preview", `{"action":"click","selector":"#go"}`); err != nil {
		t.Fatalf("click: %v", err)
	}
	if s.drive.Action != "click" || s.drive.Selector != "#go" {
		t.Fatalf("drive = %+v", s.drive)
	}
	if _, err := invoke(t, m, env, "drive_preview", `{"action":"submit"}`); err == nil {
		t.Fatal("unknown drive action succeeded")
	}
}

func TestF8TOOL031FocusPane(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{panes: []string{"preview", "terminal"}}
	m := New(s)
	env := guiEnv(t, OriginDesktop, t.TempDir())
	if _, err := invoke(t, m, env, "focus_pane", `{"pane":"preview"}`); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if s.focus != "preview" {
		t.Fatalf("focused = %q", s.focus)
	}
	_, err := invoke(t, m, env, "focus_pane", `{"pane":"../x"}`)
	wantCode(t, err, "invalid_args")
	_, err = invoke(t, m, env, "focus_pane", `{"pane":"settings"}`)
	wantCode(t, err, "not_found")
}

func TestF8TOOL032GUITour(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{}
	m := New(s)
	env := guiEnv(t, OriginWeb, t.TempDir())
	if _, err := invoke(t, m, env, "tour", `{}`); err != nil {
		t.Fatalf("legacy tour alias: %v", err)
	}
	if s.tour != "welcome" {
		t.Fatalf("tour = %q", s.tour)
	}
	if _, err := invoke(t, m, env, "gui_tour", `{"name":"first-run"}`); err != nil {
		t.Fatalf("gui_tour: %v", err)
	}
	if s.tour != "first-run" {
		t.Fatalf("tour = %q", s.tour)
	}
}

func TestF8TOOL033ReactToMessage(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{}
	m := New(s)
	env := guiEnv(t, OriginDesktop, t.TempDir())
	if _, err := invoke(t, m, env, "react_to_message", `{"message_id":"m1","emoji":"✅"}`); err != nil {
		t.Fatalf("react: %v", err)
	}
	if s.react != [2]string{"m1", "✅"} {
		t.Fatalf("react = %v", s.react)
	}
	_, err := invoke(t, m, env, "react_to_message", `{"message_id":"m1","emoji":"💣"}`)
	wantCode(t, err, "invalid_args")
}

func TestDesktopUISurfaceClosed(t *testing.T) {
	t.Parallel()
	m := New(nil)
	_, err := invoke(t, m, guiEnv(t, OriginWeb, t.TempDir()), "focus_pane", `{"pane":"preview"}`)
	wantCode(t, err, "module_unavailable")
	_, err = invoke(t, New(&fakeSurface{}), host.Env{}, "focus_pane", `{"pane":"preview"}`)
	wantCode(t, err, "denied")
	if st := m.Status(); st != host.StatusStopped {
		t.Fatalf("status before start = %q", st)
	}
	if err := m.Start(context.Background()); err != nil || m.Status() != host.StatusReady {
		t.Fatalf("start = %v status = %q", err, m.Status())
	}
	if err := m.Stop(context.Background()); err != nil || m.Status() != host.StatusStopped {
		t.Fatalf("stop = %v status = %q", err, m.Status())
	}
}

type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (s *memStore) Get(_ context.Context, key []byte) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[string(key)]
	return v, ok, nil
}

func (s *memStore) Put(_ context.Context, key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[string(key)] = append([]byte(nil), value...)
	return nil
}

func (s *memStore) Delete(_ context.Context, key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, string(key))
	return nil
}

func (s *memStore) List(_ context.Context, prefix []byte) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for k, v := range s.data {
		if strings.HasPrefix(k, string(prefix)) {
			out = append(out, v)
		}
	}
	return out, nil
}

type storeHost struct {
	host.Host
	store host.Store
}

func (h storeHost) Storage(string) (host.Store, error) { return h.store, nil }

func moduleFromStore(t *testing.T, s Surface, store host.Store) *Module {
	t.Helper()
	m, err := NewFromHost(storeHost{store: store}, json.RawMessage(`{"notices":true}`), s)
	if err != nil {
		t.Fatalf("NewFromHost: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return m
}

func TestF8TOOL034ReadTerminal(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{panes: []string{"terminal-1"}, termOut: "hello sk-live-secret-1234567890\n"}
	m := New(s)
	env := guiEnv(t, OriginWeb, t.TempDir())
	res, err := invoke(t, m, env, "read_terminal", `{"pane":"terminal-1","lines":50}`)
	if err != nil {
		t.Fatalf("read_terminal: %v", err)
	}
	if strings.Contains(res.Content, "sk-live-secret") || !strings.Contains(res.Content, "hello") {
		t.Fatalf("content not redacted: %q", res.Content)
	}
	_, err = invoke(t, m, env, "read_terminal", `{"pane":"terminal-9"}`)
	wantCode(t, err, "not_found")
	long := New(&fakeSurface{active: "terminal-1", termOut: strings.Repeat("x", maxOutputBytes+10)})
	res, err = invoke(t, long, env, "read_terminal", `{}`)
	if err != nil || !strings.Contains(res.Content, "[truncated]") || len(res.Content) > maxOutputBytes+32 {
		t.Fatalf("bounded content = %d bytes, %v", len(res.Content), err)
	}
}

func TestF8TOOL035ReadWindowBelow(t *testing.T) {
	t.Parallel()
	m := New(&fakeSurface{belowOut: "pane text"})
	env := guiEnv(t, OriginDesktop, t.TempDir())
	res, err := invoke(t, m, env, "read_window_below", `{}`)
	if err != nil || res.Content != "pane text" {
		t.Fatalf("below = %+v, %v", res, err)
	}
	gone := New(&fakeSurface{err: ErrPaneMissing})
	res, err = invoke(t, gone, env, "read_window_below", `{}`)
	if err != nil || res.Content != "" {
		t.Fatalf("missing pane = %+v, %v (want empty success)", res, err)
	}
}

func TestF8TOOL036SetTheme(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{}
	m := New(s)
	env := guiEnv(t, OriginWeb, t.TempDir())
	if _, err := invoke(t, m, env, "set_theme", `{"theme":"dark"}`); err != nil || s.theme != "dark" {
		t.Fatalf("set_theme = %v theme=%q", err, s.theme)
	}
	_, err := invoke(t, m, env, "set_theme", `{"theme":"neon"}`)
	wantCode(t, err, "invalid_args")
	if len(s.calls) != 1 {
		t.Fatalf("unknown theme reached the surface: %v", s.calls)
	}
}

func TestF8TOOL037ShowTip(t *testing.T) {
	t.Parallel()
	s := &fakeSurface{}
	env := guiEnv(t, OriginDesktop, t.TempDir())
	if _, err := invoke(t, New(s), env, "show_tip", `{"tip":"hello"}`); err != nil || s.tip != "hello" {
		t.Fatalf("tip = %v %q", err, s.tip)
	}
	quiet := &fakeSurface{}
	if res, err := invoke(t, NewWithSection(quiet, Section{}), env, "tip", `{"tip":"hello"}`); err != nil || res.Content != "" {
		t.Fatalf("notices off = %+v, %v", res, err)
	}
	if len(quiet.calls) != 0 {
		t.Fatalf("notices off reached the surface: %v", quiet.calls)
	}
}

func TestF8TOOL071SetupBeat(t *testing.T) {
	t.Parallel()
	store := newMemStore()
	first := &fakeSurface{}
	m := moduleFromStore(t, first, store)
	env := guiEnv(t, OriginWeb, t.TempDir())
	if res, err := invoke(t, m, env, "setup_beat", `{}`); err != nil || !strings.Contains(res.Content, "onboarding shown") {
		t.Fatalf("first = %+v, %v", res, err)
	}
	if res, err := invoke(t, m, env, "setup_beat", `{}`); err != nil || !strings.Contains(res.Content, "already shown") {
		t.Fatalf("second = %+v, %v", res, err)
	}
	if first.setup != 1 {
		t.Fatalf("card shown %d times", first.setup)
	}
	// A new process with the same installation store does not show it again.
	second := &fakeSurface{}
	m2 := moduleFromStore(t, second, store)
	if _, err := invoke(t, m2, env, "setup_beat", `{}`); err != nil || second.setup != 0 {
		t.Fatalf("second installation = %d, %v", second.setup, err)
	}
}

func TestF8TOOL072DesktopProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "src")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	s := &fakeSurface{}
	m := New(s)
	env := guiEnv(t, OriginDesktop, root)
	res, err := invoke(t, m, env, "desktop_project", `{"action":"create","name":"alpha","folders":["src"]}`)
	if err != nil || !strings.Contains(res.Content, "project alpha") {
		t.Fatalf("create = %+v, %v", res, err)
	}
	if s.project != "alpha" {
		t.Fatalf("surface project = %q", s.project)
	}
	if _, serr := invoke(t, m, env, "desktop_project", `{"action":"switch","name":"alpha"}`); serr != nil {
		t.Fatalf("switch: %v", serr)
	}
	_, err = invoke(t, m, env, "desktop_project", `{"action":"create","name":"beta","root":"`+outside+`"}`)
	wantCode(t, err, "outside_workspace")
	_, err = invoke(t, m, env, "desktop_project", `{"action":"switch","name":"ghost"}`)
	wantCode(t, err, "not_found")
	_, err = invoke(t, m, env, "desktop_project", `{"action":"create","name":"Bad Name"}`)
	wantCode(t, err, "invalid_args")
}
