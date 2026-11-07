package gwapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults for the server envelope.
const (
	DefaultBodyLimit = 1 << 20 // 1 MiB: prompt + tool catalog, never truncated
	DefaultTimeout   = 120 * time.Second
)

// Config is the API server envelope. APIKey is required unless
// AllowUnauthenticated is explicit; it is never logged.
type Config struct {
	APIKey               string
	AllowUnauthenticated bool
	Models               []string
	BodyLimit            int64
	Timeout              time.Duration
	MaxTokens            int
	RequestsPerMinute    int
	Clock                func() time.Time
	NewID                func() string
	// Scrub redacts secrets from inbound text and error messages.
	Scrub func(string) string
}

// Server is the OpenAI-compatible HTTP surface.
type Server struct {
	cfg    Config
	engine Engine
	tools  map[string]ToolSpec
	rl     *rateLimiter
	store  *resourceStore
}

// NewServer validates the envelope and returns the handler source.
func NewServer(cfg *Config, engine Engine, toolbox Toolbox) (*Server, error) {
	if engine == nil {
		return nil, errors.New("gwapi: engine required")
	}
	if !cfg.AllowUnauthenticated && strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("gwapi: api key required unless allow_unauthenticated is explicit")
	}
	c := *cfg
	if c.BodyLimit <= 0 {
		c.BodyLimit = DefaultBodyLimit
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.NewID == nil {
		c.NewID = newCompletionID
	}
	if c.Scrub == nil {
		c.Scrub = func(s string) string { return s }
	}
	tools := map[string]ToolSpec{}
	if toolbox != nil {
		for _, t := range toolbox.Tools() {
			if t.Name != "" {
				tools[t.Name] = t
			}
		}
	}
	return &Server{cfg: c, engine: engine, tools: tools, rl: newRateLimiter(c.RequestsPerMinute), store: newResourceStore()}, nil
}

// Handler returns the HTTP handler. CORS is off by default: no
// Access-Control-Allow-* header is ever emitted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/responses", s.handleResponsesCreate)
	mux.HandleFunc("GET /v1/responses/{id}", s.handleResponsesGet)
	mux.HandleFunc("POST /v1/responses/{id}/cancel", s.handleResponsesCancel)
	mux.HandleFunc("POST /v1/runs", s.handleRunsCreate)
	mux.HandleFunc("GET /v1/runs/{id}", s.handleRunsGet)
	mux.HandleFunc("POST /v1/runs/{id}/cancel", s.handleRunsCancel)
	mux.HandleFunc("GET /v1/runs/{id}/events", s.handleRunEvents)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleHealthz)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticate(r); err != nil {
		writeError(w, s.cfg.Scrub, err)
		return
	}
	list := ModelList{Object: "list", Data: make([]Model, 0, len(s.cfg.Models))}
	for _, id := range s.cfg.Models {
		list.Data = append(list.Data, Model{ID: id, Object: "model", Created: s.cfg.Clock().Unix(), OwnedBy: "damnbot"})
	}
	writeJSON(w, http.StatusOK, list)
}

// authenticate enforces the bearer token with a constant-time digest
// comparison. A missing or wrong token never reaches the engine.
func (s *Server) authenticate(r *http.Request) *apiError {
	if s.cfg.AllowUnauthenticated {
		return nil
	}
	raw, ok := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if !ok || strings.TrimSpace(raw) == "" {
		return &apiError{status: 401, typ: TypeAuthentication, code: "missing_api_key", message: "missing bearer token"}
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	want := sha256.Sum256([]byte(s.cfg.APIKey))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return &apiError{status: 401, typ: TypeAuthentication, code: "invalid_api_key", message: "invalid bearer token"}
	}
	return nil
}

func (s *Server) checkModel(model string) *apiError {
	if len(model) > 128 {
		return errInvalid("model", "invalid_model", "model name is too long")
	}
	if len(s.cfg.Models) == 0 || slices.Contains(s.cfg.Models, model) {
		return nil
	}
	return &apiError{status: 404, typ: TypeNotFound, code: "model_not_found", param: "model", message: "model is not configured: " + model}
}

func errInvalid(param, code, msg string) *apiError {
	return &apiError{status: 400, typ: TypeInvalidRequest, code: code, param: param, message: msg}
}

func newCompletionID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "chatcmpl-000000000000000000000000"
	}
	return "chatcmpl-" + hex.EncodeToString(b[:])
}

func defaultString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

// writeError writes the OpenAI error envelope, scrubbed of secrets.
func writeError(w http.ResponseWriter, scrub func(string) string, e *apiError) {
	if e == nil {
		e = &apiError{status: 500, typ: TypeServer, code: "internal_error", message: "internal server error"}
	}
	if e.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.retryAfter))
	}
	if e.status == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="damnbot"`)
	}
	writeJSON(w, e.status, ErrorResponse{Error: ErrorBody{
		Message: scrub(e.message), Type: defaultString(e.typ, TypeServer), Param: e.param, Code: e.code,
	}})
}

// rateLimiter is a fixed one-minute window driven by the injected clock.
type rateLimiter struct {
	limit int
	mu    sync.Mutex
	start time.Time
	used  int
}

func newRateLimiter(limit int) *rateLimiter { return &rateLimiter{limit: limit} }

func (l *rateLimiter) allow(now time.Time) *apiError {
	if l.limit <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.start.IsZero() || now.Sub(l.start) >= time.Minute {
		l.start, l.used = now, 0
	}
	if l.used >= l.limit {
		retry := max(60-int(now.Sub(l.start).Seconds()), 1)
		return &apiError{status: 429, typ: TypeRateLimit, code: "rate_limited", message: "request rate exceeded", retryAfter: retry}
	}
	l.used++
	return nil
}
