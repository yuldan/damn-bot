package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// maxClientFSBytes bounds one client buffer read: above it the full-content
// tag rule no longer applies, so a buffer cannot be tagged consistently.
const maxClientFSBytes = 8 << 20

// DefaultClientFSTimeout bounds one outbound client filesystem call.
const DefaultClientFSTimeout = 30 * time.Second

// Requester sends one JSON-RPC request to the connected client and decodes
// its result; a JSON-RPC error is returned as *RemoteError.
type Requester interface {
	Request(ctx context.Context, method string, params, result any) error
}

// ClientFS is the agent→client filesystem adapter (SPEC §13.3, GAP-UX-01):
// the agent asks the client for its editor buffer and writes new bytes back.
// It never touches disk and reports a missing capability as unsupported, so
// the caller falls back locally and records acp_fs_fallback.
type ClientFS struct {
	SessionID string
	Read      bool
	Write     bool
	Requester Requester
	Timeout   time.Duration
}

// Supports reports whether the client advertised the capability for this
// direction. A write additionally needs read: tags are computed from the
// buffer that is about to be replaced.
func (c *ClientFS) Supports(write bool) bool {
	if c == nil || c.Requester == nil {
		return false
	}
	if write {
		return c.Read && c.Write
	}
	return c.Read
}

// ReadTextFile returns the client's buffer for path. notFound means the
// client has no content for it; every other failure is returned as is.
func (c *ClientFS) ReadTextFile(ctx context.Context, path string) ([]byte, bool, error) {
	var res struct {
		Content string `json:"content"`
	}
	err := c.call(ctx, "fs/read_text_file", map[string]any{"sessionId": c.SessionID, "path": path}, &res)
	if err != nil {
		if isNotFound(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	if len(res.Content) > maxClientFSBytes {
		return nil, false, fmt.Errorf("acp client: read_text_file: buffer exceeds %d bytes", maxClientFSBytes)
	}
	return []byte(res.Content), false, nil
}

// WriteTextFile replaces the client's buffer for path.
func (c *ClientFS) WriteTextFile(ctx context.Context, path string, data []byte) error {
	if !c.Supports(true) {
		return errors.New("acp client: write_text_file capability unavailable")
	}
	return c.call(ctx, "fs/write_text_file", map[string]any{"sessionId": c.SessionID, "path": path, "content": string(data)}, nil)
}

func (c *ClientFS) call(ctx context.Context, method string, params, result any) error {
	if c == nil || c.Requester == nil {
		return errors.New("acp client: no transport")
	}
	wait := c.Timeout
	if wait <= 0 {
		wait = DefaultClientFSTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := c.Requester.Request(ctx, method, params, result); err != nil {
		return fmt.Errorf("acp client %s: %w", method, err)
	}
	return nil
}

// RemoteError is one JSON-RPC error returned by the client.
type RemoteError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("acp client: %s (%d)", e.Message, e.Code)
}

// ErrorCode reads data.error_code from the client's error (SPEC §4.3 codes).
func (e *RemoteError) ErrorCode() string {
	var d struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return ""
	}
	return d.ErrorCode
}

// isNotFound reports a client-side missing path: typed error_code or the
// resource-not-found code.
func isNotFound(err error) bool {
	var re *RemoteError
	return errors.As(err, &re) && (re.ErrorCode() == "not_found" || re.Code == -32002)
}
