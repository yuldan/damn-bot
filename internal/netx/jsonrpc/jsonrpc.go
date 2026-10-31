// Package jsonrpc is the shared JSON-RPC 2.0 framing layer for MCP and LSP:
// newline-delimited stdio, optional Content-Length LSP framing, and HTTP POST.
// It imports only the standard library and never creates an HTTP client (E-05).
package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// Version is the only accepted JSON-RPC version.
	Version = "2.0"
	// DefaultMaxFrame bounds one frame when Options.MaxFrame is zero: 1 MiB.
	DefaultMaxFrame = 1 << 20
	// CancelMethod is the LSP cancellation notification.
	CancelMethod = "$/cancelRequest"
)

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInternalError  = -32603
)

// Framing and lifecycle sentinels.
var (
	ErrFrameTooLarge   = errors.New("jsonrpc: frame exceeds maximum size")
	ErrInvalidFrame    = errors.New("jsonrpc: invalid frame")
	ErrInvalidRequest  = errors.New("jsonrpc: invalid request")
	ErrInvalidResponse = errors.New("jsonrpc: invalid response")
	ErrClosed          = errors.New("jsonrpc: connection closed")
	ErrPeerClosed      = errors.New("jsonrpc: peer closed")
)

// Error is a JSON-RPC error object; Data must never carry secrets or raw input.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// Message is one request, notification, or response; ID is a raw JSON scalar.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

func (m *Message) hasID() bool {
	trimmed := bytes.TrimSpace(m.ID)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null"))
}
func idKey(id json.RawMessage) string { return string(bytes.TrimSpace(id)) }
func marshalRaw(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, fmt.Errorf("%w: raw JSON is invalid", ErrInvalidRequest)
		}
		return raw, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: marshal value: %w", err)
	}
	return b, nil
}

func newMessage(method string, params any, id json.RawMessage) (Message, error) {
	if method == "" {
		return Message{}, fmt.Errorf("%w: empty method", ErrInvalidRequest)
	}
	raw, err := marshalRaw(params)
	if err != nil {
		return Message{}, err
	}
	return Message{JSONRPC: Version, ID: id, Method: method, Params: raw}, nil
}

func decodeResponse(m *Message, result any) error {
	if m.Error != nil {
		return m.Error
	}
	if m.Result == nil {
		return fmt.Errorf("%w: response has neither result nor error", ErrInvalidResponse)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(m.Result, result); err != nil {
		return fmt.Errorf("%w: result: %w", ErrInvalidResponse, err)
	}
	return nil
}
