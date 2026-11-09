package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// MaxAssetBytes is the SPEC §13.2 ceiling for the embedded asset set.
const MaxAssetBytes = 1_500_000

//go:embed assets
var embeddedAssets embed.FS

// defaultAssets is the embedded asset tree rooted at assets/.
func defaultAssets() (fs.FS, error) {
	sub, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		return nil, fmt.Errorf("web: embedded assets: %w", err)
	}
	return sub, nil
}

// templateSet is parsed once at startup.
type templateSet struct {
	index    *template.Template
	auth     *template.Template
	settings *template.Template
}

func parseTemplates(fsys fs.FS) (*templateSet, error) {
	parse := func(name string) (*template.Template, error) {
		t, err := template.ParseFS(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("web: %s template: %w", name, err)
		}
		return t, nil
	}
	index, err := parse("index.html")
	if err != nil {
		return nil, err
	}
	auth, err := parse("login.html")
	if err != nil {
		return nil, err
	}
	settings, err := parse("settings.html")
	if err != nil {
		return nil, err
	}
	return &templateSet{index: index, auth: auth, settings: settings}, nil
}

// pageData is the only template input; it never carries a session token.
type pageData struct {
	CSRF     string
	Language string
	Locales  []string
}

// render writes one HTML page; a template failure is a plain 500.
func (s *Server) render(w http.ResponseWriter, t *template.Template, data pageData) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		s.logWarn("render", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(buf.Bytes()); err != nil {
		s.logWarn("write page", err)
	}
}

// asset serves the embedded ES modules and stylesheets.
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	if !servableAsset(name) {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, name, s.now(), bytes.NewReader(data))
}

// servableAsset admits the static-asset extensions of the embedded set.
func servableAsset(name string) bool {
	if name == "" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return false
	}
	switch path.Ext(name) {
	case ".js", ".css", ".svg", ".woff2":
		return true
	default:
		return false
	}
}
