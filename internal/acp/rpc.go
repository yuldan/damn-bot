package acp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ProtocolVersion is the ACP protocol version this server speaks (SPEC §13.3).
const ProtocolVersion = 1

// JSON-RPC 2.0 error codes (SPEC §13.3).
const (
	CodeParseError       = -32700
	CodeInvalidRequest   = -32600
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodeInternal         = -32603
	CodeResourceNotFound = -32002
)

// Request is one JSON-RPC 2.0 request or notification. ID is nil when the
// frame is a notification; any other RawMessage (including null) is answered.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ErrWriteTimeout reports that a frame could not be written inside the write
// deadline; the caller closes the connection (SPEC §13.3).
var ErrWriteTimeout = errors.New("acp: write deadline exceeded")

// Error is one JSON-RPC error object. Messages are fixed protocol text: they
// never include params, arguments, or a method name supplied by the client, so
// a malformed frame cannot smuggle a secret into the response (SPEC §13.3).
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("acp: %s (%d)", e.Message, e.Code) }

// Response is one JSON-RPC response. Exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Protocol errors. Messages are constants; callers pass no user text.
var (
	ErrParse            = &Error{Code: CodeParseError, Message: "parse error"}
	ErrRequest          = &Error{Code: CodeInvalidRequest, Message: "invalid request"}
	ErrMethod           = &Error{Code: CodeMethodNotFound, Message: "method not found"}
	ErrParams           = &Error{Code: CodeInvalidParams, Message: "invalid params"}
	ErrInternal         = &Error{Code: CodeInternal, Message: "internal error"}
	ErrResourceNotFound = &Error{Code: CodeResourceNotFound, Message: "resource not found"}
	// ErrFrameLimit is the typed answer to a frame above MaxFrameBytes; the
	// reader has already drained the rest of that line (SPEC §13.3).
	ErrFrameLimit = &Error{Code: CodeInvalidRequest, Message: "frame exceeds 16 MiB"}
)

// ErrorWithData returns a copy of e carrying a JSON data object. The data is
// built by the caller from closed error codes only; it never echoes client
// text (SPEC §13.3).
func ErrorWithData(e *Error, data map[string]any) *Error {
	out := *e
	b, err := json.Marshal(data)
	if err != nil {
		return &out
	}
	out.Data = b
	return &out
}

// ParseRequest decodes one frame into a Request. It returns a protocol error
// (never a transport error) for malformed frames; the caller may answer it
// with a null id.
func ParseRequest(frame []byte) (Request, *Error) {
	if len(frame) == 0 {
		return Request{}, ErrRequest
	}
	if frame[0] == '[' {
		// Batch requests are not part of ACP; reject rather than mis-answer.
		return Request{}, ErrRequest
	}
	var req Request
	if err := json.Unmarshal(frame, &req); err != nil {
		return Request{}, ErrParse
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		return Request{}, ErrRequest
	}
	if !validID(req.ID) {
		return Request{}, ErrRequest
	}
	if len(req.Params) > 0 && !validStructured(req.Params) {
		return Request{}, ErrParams
	}
	return req, nil
}

// validID accepts an absent id, null, a string, or a number (JSON-RPC 2.0).
func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	var v any
	if err := json.Unmarshal(id, &v); err != nil {
		return false
	}
	switch v.(type) {
	case nil, string, float64:
		return true
	}
	return false
}

// validStructured accepts the object or array form JSON-RPC allows for params.
func validStructured(raw json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// EncodeResponse returns the wire frame (JSON plus LF) for resp.
func EncodeResponse(resp *Response) ([]byte, error) {
	if resp.JSONRPC == "" {
		resp.JSONRPC = "2.0"
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("acp: encode response: %w", err)
	}
	return append(b, '\n'), nil
}
