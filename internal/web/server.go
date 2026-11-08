// Package web is the `damnbot serve --web` listener (SPEC §13.2, F8): a
// loopback HTTP server with the SPEC security headers. The authenticated API,
// the embedded assets and the SSE stream land in the next slices; this one
// serves /healthz only, so nothing sensitive is reachable before auth exists.
//
// Security is fail-closed (GAP-SEC-06/08): every response carries the SPEC CSP
// with a per-request nonce, and a non-loopback bind needs allow_remote plus
// TLS.
package web

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Errors returned by [New] before anything listens.
var (
	// ErrRemoteRefused is a non-loopback listen without allow_remote.
	ErrRemoteRefused = errors.New("web: non-loopback listen requires allow_remote")
	// ErrRemoteTLS is a non-loopback listen without a certificate pair.
	ErrRemoteTLS = errors.New("web: non-loopback listen requires TLS")
	// ErrListen is a malformed listen address.
	ErrListen = errors.New("web: invalid listen address")
)

// Config is the resolved `[web]` section. IO is injected by cmd; the package
// never reads the environment.
type Config struct {
	// Listen is host:port or unix:<path>; the path may contain <state_dir>.
	Listen      string
	AllowRemote bool
	// AllowedHosts is the Host allowlist for a non-loopback bind.
	AllowedHosts []string
	TLSCert      string
	TLSKey       string
	// DataDir expands the <state_dir> placeholder of a unix listen.
	DataDir string
	// Now is the clock seam; the default is time.Now.
	Now func() time.Time
	// Log receives operational warnings; nil is silent.
	Log *slog.Logger
}

// Server is one web listener. It is safe for concurrent use.
type Server struct {
	cfg Config
	now func() time.Time
	srv *http.Server
	ln  net.Listener
}

// New validates cfg and builds the handler. Nothing listens until [Server.Serve].
func New(cfg *Config) (*Server, error) {
	c := *cfg
	if c.Now == nil {
		c.Now = time.Now
	}
	local, err := listenIsLocal(c.Listen)
	if err != nil {
		return nil, err
	}
	if !local {
		if !c.AllowRemote {
			return nil, ErrRemoteRefused
		}
		if c.TLSCert == "" || c.TLSKey == "" {
			return nil, ErrRemoteTLS
		}
	}
	s := &Server{cfg: c, now: c.Now}
	s.srv = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
	}
	if c.Log != nil {
		s.srv.ErrorLog = slog.NewLogLogger(c.Log.Handler(), slog.LevelWarn)
	}
	return s, nil
}

// listenIsLocal reports whether the configured bind is loopback or a unix
// socket, whose peer UID the server checks.
func listenIsLocal(listen string) (bool, error) {
	if raw, ok := strings.CutPrefix(listen, "unix:"); ok {
		if _, ok := strings.CutPrefix(raw, "<state_dir>/"); ok {
			return true, nil
		}
		if !filepath.IsAbs(raw) || strings.ContainsAny(raw, "\x00\r\n") {
			return false, fmt.Errorf("%w: unix socket path must be absolute", ErrListen)
		}
		return true, nil
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return false, fmt.Errorf("%w: %q is not host:port or unix:<path>", ErrListen, listen)
	}
	if host == "localhost" {
		return true, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}
	return false, nil
}

// Serve accepts connections on ln until Shutdown. It returns nil on a clean
// stop.
func (s *Server) Serve(ln net.Listener) error {
	var err error
	if s.cfg.TLSCert != "" {
		var cert tls.Certificate
		if cert, err = tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey); err != nil {
			return fmt.Errorf("web: load TLS pair: %w", err)
		}
		s.srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	if err = s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web: serve: %w", err)
	}
	return nil
}

// Listen opens the configured listener. A unix socket is created 0600 after a
// stale socket of a dead process is removed.
func (s *Server) Listen(ctx context.Context) (net.Listener, error) {
	if strings.HasPrefix(s.cfg.Listen, "unix:") {
		ln, err := s.listenUnix(ctx)
		if err == nil {
			s.ln = ln
		}
		return ln, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("web: listen: %w", err)
	}
	s.ln = ln
	return ln, nil
}

// listenUnix creates the 0600 unix socket (GAP-SEC-07).
func (s *Server) listenUnix(ctx context.Context) (net.Listener, error) {
	raw := strings.TrimPrefix(s.cfg.Listen, "unix:")
	path := strings.Replace(raw, "<state_dir>", s.cfg.DataDir, 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("web: socket dir: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("web: stale socket: %w", err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("web: listen unix: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		if cerr := ln.Close(); cerr != nil {
			s.logWarn("close socket listener", cerr)
		}
		return nil, fmt.Errorf("web: socket mode: %w", err)
	}
	return ln, nil
}

// Shutdown stops the listener and drains active requests.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("web: shutdown: %w", err)
	}
	return nil
}

// Addr is the bound address (empty before Listen/Serve).
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// logWarn is a nil-safe operational warning; it never carries a token.
func (s *Server) logWarn(msg string, err error) {
	if s.cfg.Log != nil {
		s.cfg.Log.Warn("web", "op", msg, "err", err)
	}
}
