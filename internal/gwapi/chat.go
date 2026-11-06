package gwapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// handleChat serves one completion in stream or non-stream form.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if err := s.authenticate(r); err != nil {
		writeError(w, s.cfg.Scrub, err)
		return
	}
	if err := s.rl.allow(s.cfg.Clock()); err != nil {
		writeError(w, s.cfg.Scrub, err)
		return
	}
	if r.ContentLength > s.cfg.BodyLimit {
		writeError(w, s.cfg.Scrub, tooLarge())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.BodyLimit))
	if err != nil {
		writeError(w, s.cfg.Scrub, tooLarge())
		return
	}
	req, derr := decodeRequest(body)
	if derr != nil {
		writeError(w, s.cfg.Scrub, derr)
		return
	}
	if merr := s.checkModel(req.Model); merr != nil {
		writeError(w, s.cfg.Scrub, merr)
		return
	}
	names, terr := s.resolveTools(req)
	if terr != nil {
		writeError(w, s.cfg.Scrub, terr)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	turn := &Turn{
		SessionID: sessionIDFor(r, req, s.cfg.NewID),
		Model:     req.Model,
		Messages:  req.Messages,
		Tools:     names,
		MaxTokens: intOr(req.MaxTokens, s.cfg.MaxTokens),
		Meta:      map[string]string{"origin": "api_server", "tainted": "true"},
	}
	s.scrubTurn(turn)
	stream, rerr := s.engine.Run(ctx, turn)
	if rerr != nil {
		writeError(w, s.cfg.Scrub, asAPIError(rerr))
		return
	}
	if stream == nil {
		writeError(w, s.cfg.Scrub, &apiError{status: 500, typ: TypeServer, code: "internal_error", message: "engine returned no stream"})
		return
	}
	if req.Stream {
		s.serveStream(ctx, w, req, stream, turn.Model)
		return
	}
	s.serveJSON(ctx, w, stream, turn.Model)
}

// resolveTools maps request tools onto the safe internal bundle.
func (s *Server) resolveTools(req *ChatRequest) ([]string, *apiError) {
	if toolChoiceNone(req.ToolChoice) || len(req.Tools) == 0 {
		return nil, nil
	}
	if len(s.tools) == 0 {
		return nil, errInvalid("tools", "tools_not_supported", "this server exposes no tools")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(req.Tools))
	for i := range req.Tools {
		name := req.Tools[i].Function.Name
		spec, ok := s.tools[name]
		if !ok {
			return nil, errInvalid("tools", "unknown_tool", "tool is not in the api-server bundle: "+name)
		}
		if spec.ApprovalRequired {
			return nil, &apiError{
				status: 403, typ: TypePermission, code: "approval_required", param: "tools",
				message: "tool requires interactive approval and cannot run through the API: " + name,
			}
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

func (s *Server) scrubTurn(turn *Turn) {
	for i := range turn.Messages {
		m := &turn.Messages[i]
		m.Content = s.cfg.Scrub(m.Content)
		for j := range m.ToolCalls {
			m.ToolCalls[j].Function.Arguments = s.cfg.Scrub(m.ToolCalls[j].Function.Arguments)
		}
	}
}

func toolChoiceNone(raw json.RawMessage) bool {
	var v string
	return json.Unmarshal(raw, &v) == nil && v == "none"
}

// sessionIDFor binds a request to a stable session for multi-turn clients.
func sessionIDFor(r *http.Request, req *ChatRequest, newID func() string) string {
	if id := strings.TrimSpace(r.Header.Get("X-Damnbot-Session")); validSessionID(id) {
		return id
	}
	if req.User != "" {
		sum := sha256.Sum256([]byte(req.User + "\x00" + req.Model))
		return "api-" + hex.EncodeToString(sum[:8])
	}
	return "api-" + newID()
}

func validSessionID(id string) bool {
	if len(id) < 4 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func intOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

func tooLarge() *apiError {
	return &apiError{status: 413, typ: TypeTooLarge, code: "request_too_large", message: "request body exceeds the configured limit"}
}

// RunError is a typed engine failure carrying the HTTP status the server
// must return. An unknown model is never surfaced as a generic 500.
type RunError struct {
	Status     int
	Type       string
	Code       string
	Param      string
	Message    string
	RetryAfter int
}

func (e *RunError) Error() string { return e.Message }

// asAPIError converts an engine failure to the wire error, defaulting to 500.
func asAPIError(err error) *apiError {
	if re, ok := errors.AsType[*RunError](err); ok {
		return &apiError{
			status: re.Status, typ: defaultString(re.Type, TypeServer), code: re.Code,
			param: re.Param, message: defaultString(re.Error(), "engine failure"), retryAfter: re.RetryAfter,
		}
	}
	return &apiError{status: 500, typ: TypeServer, code: "internal_error", message: "internal server error"}
}
