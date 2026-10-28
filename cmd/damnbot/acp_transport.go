package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/acp"
)

// clientFS reports the client filesystem capabilities from initialize.
func (s *acpServer) clientFS() (read, write bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fsRead, s.fsWrite
}

// Request sends one agent→client JSON-RPC request and decodes the result
// (GAP-UX-01). Client errors come back as *acp.RemoteError.
func (s *acpServer) Request(ctx context.Context, method string, params, result any) error {
	id := fmt.Sprintf("req-%d", s.bumpReq())
	ch := make(chan acpReply, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	if err := s.write(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case reply := <-ch:
		if len(reply.Error) > 0 {
			var re acp.RemoteError
			if err := json.Unmarshal(reply.Error, &re); err != nil {
				return acp.ErrInternal
			}
			return &re
		}
		if result == nil {
			return nil
		}
		if len(reply.Result) == 0 {
			return acp.ErrInternal
		}
		if err := json.Unmarshal(reply.Result, result); err != nil {
			return fmt.Errorf("acp: client result: %w", err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("acp: client request: %w", ctx.Err())
	}
}

func (s *acpServer) bumpReq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqSeq++
	return s.reqSeq
}

func (s *acpServer) bumpPerm() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permSeq++
	return s.permSeq
}

func (s *acpServer) routeResponse(frame []byte) bool {
	var r struct {
		ID json.RawMessage `json:"id"`
		acpReply
	}
	if json.Unmarshal(frame, &r) != nil || len(r.ID) == 0 {
		return false
	}
	var id string
	if json.Unmarshal(r.ID, &id) != nil {
		return false
	}
	s.mu.Lock()
	ch := s.pending[id]
	s.mu.Unlock()
	if ch == nil {
		return false
	}
	ch <- r.acpReply
	return true
}

func (s *acpServer) respond(ctx context.Context, id json.RawMessage, result any) {
	if err := s.write(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		return
	}
}

func (s *acpServer) notify(ctx context.Context, params any) {
	if err := s.write(ctx, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": params}); err != nil {
		return
	}
}

func (s *acpServer) writeError(ctx context.Context, id json.RawMessage, e *acp.Error) {
	if err := s.write(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "error": e}); err != nil {
		return
	}
}
