package gwpeer

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// maxPeerBody bounds one DM request. Text itself is capped tighter by
	// maxTextBytes; the extra room covers JSON escaping.
	maxPeerBody = 160 << 10
	// peerAuthHeader names the sending peer; the token authenticates it.
	peerAuthHeader = "X-Damnbot-Peer"
)

// Server is the inbound peer HTTP surface. Dispatch is called exactly once per
// accepted message id; a failed dispatch can be retried with a fresh nonce.
type Server struct {
	ID       string
	Peers    func(id string) (Peer, bool)
	Tokens   TokenSource
	Guard    *ReplayGuard
	Dispatch func(ctx context.Context, env *Envelope) error
	Now      func() time.Time
	Window   time.Duration
	// StopToken guards the loopback-only admin endpoint; empty disables it.
	StopToken string
	Stop      func()

	guardOnce sync.Once
}

// Handler returns the peer HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/v1/dm", s.handleDM)
	return mux
}

// Serve runs the server on ln until ctx is cancelled or the admin stop
// endpoint fires.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				return
			}
		case <-done:
		}
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("gwpeer: serve: %w", err)
	}
	return nil
}

func (s *Server) handleDM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	peer, ok := s.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	env, err := decodeEnvelope(r)
	if err != nil {
		http.Error(w, "bad envelope", http.StatusBadRequest)
		return
	}
	if err := s.accept(r.Context(), &peer, env); err != nil {
		s.reject(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": env.ID})
}

// authenticate resolves the header peer and compares the bearer token in
// constant time. A missing or unknown peer is unauthorized, never anonymous.
func (s *Server) authenticate(r *http.Request) (Peer, bool) {
	if s.Peers == nil || s.Tokens == nil {
		return Peer{}, false
	}
	id := strings.TrimSpace(r.Header.Get(peerAuthHeader))
	peer, ok := s.Peers(id)
	if !ok {
		return Peer{}, false
	}
	token, err := s.Tokens.Token(r.Context(), peer.TokenRef)
	if err != nil || token == "" {
		return Peer{}, false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return Peer{}, false
	}
	got := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	return peer, subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// decodeEnvelope reads one bounded JSON envelope.
func decodeEnvelope(r *http.Request) (*Envelope, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPeerBody+1))
	if err != nil {
		return nil, fmt.Errorf("gwpeer: read envelope: %w", err)
	}
	if len(body) > maxPeerBody {
		return nil, fmt.Errorf("%w: body too large", ErrInvalidEnvelope)
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEnvelope, err)
	}
	return &env, nil
}

// accept validates, authenticates, dedupes, and dispatches one envelope.
func (s *Server) accept(ctx context.Context, peer *Peer, env *Envelope) error {
	now := s.now()
	if err := env.Validate(now, s.window()); err != nil {
		return err
	}
	if env.To != s.ID || env.From != peer.ID {
		return fmt.Errorf("%w: %s -> %s", ErrUnknownPeer, env.From, env.To)
	}
	if err := env.Verify(peer.PublicKey); err != nil {
		return err
	}
	guard := s.guard()
	if err := guard.Reserve(env); err != nil {
		return err
	}
	if err := s.dispatch(ctx, env); err != nil {
		return err
	}
	guard.Commit(env)
	return nil
}

func (s *Server) dispatch(ctx context.Context, env *Envelope) error {
	if s.Dispatch == nil {
		return nil
	}
	if err := s.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("gwpeer: dispatch %s: %w", env.ID, err)
	}
	return nil
}

func (s *Server) reject(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrReplay), errors.Is(err, ErrDuplicate):
		http.Error(w, "conflict", http.StatusConflict)
	case errors.Is(err, ErrSignature), errors.Is(err, ErrUnknownPeer), errors.Is(err, ErrExpired), errors.Is(err, ErrInvalidEnvelope):
		http.Error(w, "bad request", http.StatusBadRequest)
	default:
		http.Error(w, "dispatch failed", http.StatusServiceUnavailable)
	}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) window() time.Duration {
	if s.Window <= 0 {
		return DefaultReplayWindow
	}
	return s.Window
}

func (s *Server) guard() *ReplayGuard {
	s.guardOnce.Do(func() {
		if s.Guard == nil {
			s.Guard = NewReplayGuard(s.Now, s.Window)
		}
	})
	return s.Guard
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent; the connection simply closes.
		return
	}
}
