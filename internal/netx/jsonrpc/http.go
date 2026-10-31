package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
)

// HTTPClient sends one JSON-RPC message per HTTP POST. The caller owns the
// [http.Client] (E-05): this package never constructs a transport or dialer.
type HTTPClient struct {
	url    string
	client *http.Client
	opts   Options
	seq    atomic.Uint64
}

// NewHTTPClient binds a POST endpoint to a caller-owned client.
func NewHTTPClient(url string, client *http.Client, opts Options) (*HTTPClient, error) {
	if url == "" || client == nil {
		return nil, fmt.Errorf("%w: url and client are required", ErrInvalidRequest)
	}
	return &HTTPClient{url: url, client: client, opts: opts}, nil
}

// Call sends one request and decodes the matching response.
func (h *HTTPClient) Call(ctx context.Context, method string, params, result any) error {
	id := json.RawMessage(strconv.FormatUint(h.seq.Add(1), 10))
	msg, err := newMessage(method, params, id)
	if err != nil {
		return err
	}
	out, err := h.post(ctx, &msg)
	if err != nil {
		return err
	}
	if idKey(out.ID) != idKey(id) {
		return fmt.Errorf("%w: response id %s does not match %s", ErrInvalidResponse, out.ID, id)
	}
	return decodeResponse(&out, result)
}

// Notify sends one notification. A 2xx response with an empty body is the
// normal outcome.
func (h *HTTPClient) Notify(ctx context.Context, method string, params any) error {
	msg, err := newMessage(method, params, nil)
	if err != nil {
		return err
	}
	_, err = h.post(ctx, &msg)
	return err
}

func (h *HTTPClient) post(ctx context.Context, msg *Message) (Message, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return Message{}, fmt.Errorf("jsonrpc: marshal message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return Message{}, fmt.Errorf("jsonrpc: new request: %w", err)
	}
	req.Header = http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}
	resp, err := h.client.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("jsonrpc: post: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(h.opts.maxFrame())+1))
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return Message{}, fmt.Errorf("jsonrpc: response body: %w", err)
	}
	if len(data) > h.opts.maxFrame() {
		return Message{}, ErrFrameTooLarge
	}
	if len(data) == 0 {
		return Message{}, nil
	}
	var out Message
	if err := json.Unmarshal(data, &out); err != nil {
		return Message{}, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
	}
	return out, nil
}
