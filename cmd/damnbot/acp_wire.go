package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/damnbot/damnbot/internal/acp"
	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tui"
)

// acpRunner is the loop seam the ACP server drives; *tuiRunner implements it.
type acpRunner interface {
	Prompt(ctx context.Context, sessionID string, in tui.Input) (<-chan tui.Update, error)
	Cancel(sessionID string)
	Close() error
}

type acpSession struct {
	proto  *acp.Session
	runner acpRunner
}

type acpServer struct {
	cfg      *config.Config
	deps     tuiDeps
	writer   frameWriter
	mu       sync.Mutex
	wg       sync.WaitGroup
	sessions map[string]*acpSession
	pending  map[string]chan json.RawMessage
	permSeq  int
	nextID   func() (string, error)
	factory  func(string, *tuiDeps) acpRunner
}

const acpProtocolVersion = 1

type frameWriter interface {
	WriteFrame(context.Context, []byte) error
}

type streamWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *streamWriter) WriteFrame(_ context.Context, frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.w.Write(frame)
	if err != nil {
		return fmt.Errorf("acp write: %w", err)
	}
	return nil
}

func newACPServer(cfg *config.Config, deps *tuiDeps, writer frameWriter, nextID func() (string, error), factory func(string, *tuiDeps) acpRunner) *acpServer {
	return &acpServer{
		cfg: cfg, deps: *deps, writer: writer,
		sessions: map[string]*acpSession{}, pending: map[string]chan json.RawMessage{}, nextID: nextID, factory: factory,
	}
}

func runACP(args []string, _ io.Writer, stderr io.Writer) int {
	if len(args) != 0 {
		say(stderr, "acp: unexpected argument %q\n", cleanArg(args[0]))
		return 2
	}
	o := &runOptions{sessionOrigin: "acp", approvalOrigin: loop.OriginACP, approval: "ask"}
	cfg, dataDir, configDir, home, err := loadRunEnv(o)
	if err != nil {
		say(stderr, "acp: %s\n", cleanLine(err.Error()))
		return 1
	}
	deps := tuiDeps{cfg: &cfg, dataDir: dataDir, configDir: configDir, home: home, opts: o, red: redact.New(redact.Options{}), modelRef: cfg.Provider.Model}
	srv := newACPServer(&cfg, &deps, &streamWriter{w: os.Stdout}, newSessionID, func(sid string, d *tuiDeps) acpRunner {
		opts := *d.opts
		opts.newSessionID = sid
		dd := *d
		dd.opts = &opts
		return newTUIRunner(&dd)
	})
	if f, ok := any(os.Stdout).(*os.File); ok {
		srv.writer = acp.FileWriter{F: f}
	}
	if err := srv.serve(context.Background(), os.Stdin); err != nil {
		say(stderr, "acp: %s\n", cleanLine(err.Error()))
		return 1
	}
	return 0
}

func (s *acpServer) serve(ctx context.Context, in io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rd := acp.NewReader(in)
	for {
		frame, err := rd.ReadFrame()
		if errors.Is(err, io.EOF) {
			cancel()
			s.wg.Wait()
			s.closeAll()
			return nil
		}
		if err != nil {
			return fmt.Errorf("acp read: %w", err)
		}
		if len(frame) == 0 {
			continue
		}
		s.handle(ctx, frame)
	}
}

func (s *acpServer) handle(ctx context.Context, frame []byte) {
	req, perr := acp.ParseRequest(frame)
	if perr != nil {
		if s.routeResponse(frame) {
			return
		}
		s.writeError(ctx, nil, perr)
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion int `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &p); err != nil {
				s.writeError(ctx, req.ID, acp.ErrParams)
				return
			}
		}
		proto := acpProtocolVersion
		if p.ProtocolVersion == proto {
			proto = p.ProtocolVersion
		}
		s.respond(ctx, req.ID, map[string]any{
			"protocolVersion":   proto,
			"agentInfo":         map[string]any{"name": "damnbot", "version": version},
			"agentCapabilities": map[string]any{"loadSession": false, "promptCapabilities": map[string]any{"image": false, "audio": false, "embeddedContext": true}, "mcpCapabilities": map[string]any{"http": false, "sse": false}},
			"authMethods":       []any{},
		})
	case "session/new":
		s.newSession(ctx, &req)
	case "session/prompt":
		s.wg.Add(1)
		_ = boundary.Go(ctx, "acp.prompt", func(ctx context.Context) error {
			defer s.wg.Done()
			s.prompt(ctx, &req)
			return nil
		})
	case "session/cancel":
		s.cancel(&req)
	case "session/set_mode":
		s.setMode(ctx, &req)
	default:
		s.writeError(ctx, req.ID, acp.ErrMethod)
	}
}

func (s *acpServer) newSession(ctx context.Context, req *acp.Request) {
	id, err := s.nextID()
	if err != nil {
		s.writeError(ctx, req.ID, acp.ErrInternal)
		return
	}
	proto, err := acp.NewSession(id, 0)
	if err != nil {
		s.writeError(ctx, req.ID, acp.ErrInternal)
		return
	}
	runner := s.factory(id, &s.deps)
	bridge := &approvalBridge{}
	s.wireBridge(ctx, bridge, id)
	s.mu.Lock()
	s.sessions[id] = &acpSession{proto: proto, runner: runner}
	s.mu.Unlock()
	result := map[string]any{
		"sessionId": id,
		"modes": map[string]any{
			"currentModeId": acp.ModeAsk.String(),
			"availableModes": []any{
				map[string]any{"id": acp.ModeAsk.String(), "name": "Ask"},
				map[string]any{"id": acp.ModeAutoEdit.String(), "name": "Auto Edit"},
				map[string]any{"id": acp.ModeAuto.String(), "name": "Auto"},
				map[string]any{"id": acp.ModePlan.String(), "name": "Plan"},
			},
		},
	}
	if s.deps.modelRef != "" {
		result["models"] = map[string]any{
			"currentModelId":  s.deps.modelRef,
			"availableModels": []any{map[string]any{"modelId": s.deps.modelRef, "name": s.deps.modelRef}},
		}
	}
	s.respond(ctx, req.ID, result)
}

func (s *acpServer) prompt(ctx context.Context, req *acp.Request) {
	var p struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.SessionID == "" {
		s.writeError(ctx, req.ID, acp.ErrParams)
		return
	}
	sess := s.get(p.SessionID)
	if sess == nil || sess.proto.State() == acp.StateClosed {
		s.writeError(ctx, req.ID, acp.ErrParams)
		return
	}
	if err := sess.proto.Activate(); err != nil {
		s.writeError(ctx, req.ID, acp.ErrInternal)
		return
	}
	var text strings.Builder
	for _, c := range p.Prompt {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	stream, err := sess.runner.Prompt(ctx, p.SessionID, tui.Input{Text: text.String(), PlanMode: sess.proto.PlanMode()})
	if err != nil {
		s.writeError(ctx, req.ID, acp.ErrInternal)
		return
	}
	stop := "end_turn"
	for up := range stream {
		switch up.Kind {
		case tui.KindTextDelta:
			s.notify(ctx, "session/update", map[string]any{"sessionId": p.SessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": up.Text}}})
		case tui.KindToolCallStart, tui.KindToolCallEnd, tui.KindToolResult:
			s.notify(ctx, "session/update", map[string]any{"sessionId": p.SessionID, "update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": up.ToolCallID, "status": up.Status, "content": up.Text}})
		case tui.KindNotice:
			s.notify(ctx, "session/update", map[string]any{"sessionId": p.SessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": up.Text}}})
		case tui.KindToolCallArgs, tui.KindUsage:
			continue
		case tui.KindStop:
			stop = acpStop(string(up.Detail), string(up.Stop))
		}
	}
	s.respond(ctx, req.ID, map[string]any{"stopReason": stop})
}

func (s *acpServer) cancel(req *acp.Request) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		return
	}
	if sess := s.get(p.SessionID); sess != nil {
		sess.runner.Cancel(p.SessionID)
	}
}

func (s *acpServer) setMode(ctx context.Context, req *acp.Request) {
	var p struct {
		SessionID string `json:"sessionId"`
		Mode      string `json:"modeId"`
	}
	if json.Unmarshal(req.Params, &p) != nil {
		s.writeError(ctx, req.ID, acp.ErrParams)
		return
	}
	mode, err := acp.ParseMode(p.Mode)
	if err != nil || s.get(p.SessionID) == nil {
		s.writeError(ctx, req.ID, acp.ErrParams)
		return
	}
	if err := s.get(p.SessionID).proto.SetMode(mode); err != nil {
		s.writeError(ctx, req.ID, acp.ErrParams)
		return
	}
	s.respond(ctx, req.ID, nil)
}

func acpStop(detail, stop string) string {
	if detail == "content_filtered" || stop == "content_filtered" {
		return "refusal"
	}
	if stop == "cancelled" || detail == "cancelled" {
		return "cancelled"
	}
	return "end_turn"
}

func (s *acpServer) get(id string) *acpSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *acpServer) closeAll() {
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = map[string]*acpSession{}
	s.mu.Unlock()
	for _, sess := range sessions {
		if err := sess.runner.Close(); err != nil {
			continue
		}
		if err := sess.proto.Close(); err != nil {
			continue
		}
	}
}

func (s *acpServer) wireBridge(ctx context.Context, b *approvalBridge, sid string) {
	b.setSender(func(msg tea.Msg) {
		m, ok := msg.(tui.ApprovalMsg)
		if !ok || m.Reply == nil {
			return
		}
		_ = boundary.Go(ctx, "acp.permission", func(ctx context.Context) error {
			m.Reply <- s.askPermission(ctx, sid, &m.Request)
			return nil
		})
	})
}

func (s *acpServer) askPermission(ctx context.Context, sid string, r *tui.ApprovalRequest) tui.ApprovalDecision {
	req := acp.PermissionRequest{
		SessionID: sid, Tool: r.Tool, Class: r.Effect, Effect: r.Effect,
		Preview: r.Preview, Origin: r.Origin, Args: json.RawMessage(r.Args), AllowSession: !r.NoSession,
	}
	if err := req.Validate(); err != nil {
		return tui.ApprovalDeny
	}
	id := fmt.Sprintf("perm-%d", s.bumpPerm())
	ch := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	if err := s.write(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/request_permission", "params": req.Params()}); err != nil {
		return tui.ApprovalDeny
	}
	wait := time.Duration(s.cfg.Approval.PreviewTimeoutS) * time.Second
	if wait <= 0 {
		wait = 600 * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	select {
	case raw := <-ch:
		ans, err := acp.DecodePermissionResult(raw)
		if err != nil {
			return tui.ApprovalDeny
		}
		switch ans {
		case acp.PermissionAllowOnce:
			return tui.ApprovalAllowOnce
		case acp.PermissionAllowAlways:
			return tui.ApprovalAllowSession
		case acp.PermissionReject:
			return tui.ApprovalDeny
		default:
			return tui.ApprovalDeny
		}
	case <-tctx.Done():
		return tui.ApprovalDeny
	}
}

func (s *acpServer) bumpPerm() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permSeq++
	return s.permSeq
}

func (s *acpServer) routeResponse(frame []byte) bool {
	var r struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
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
	ch <- r.Result
	return true
}

func (s *acpServer) respond(ctx context.Context, id json.RawMessage, result any) {
	if err := s.write(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "result": result}); err != nil {
		return
	}
}

func (s *acpServer) notify(ctx context.Context, method string, params any) {
	if err := s.write(ctx, map[string]any{"jsonrpc": "2.0", "method": method, "params": params}); err != nil {
		return
	}
}

func (s *acpServer) writeError(ctx context.Context, id json.RawMessage, e *acp.Error) {
	if err := s.write(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "error": e}); err != nil {
		return
	}
}

func (s *acpServer) write(ctx context.Context, v any) error {
	frame, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("acp encode: %w", err)
	}
	if err := s.writer.WriteFrame(ctx, append(frame, '\n')); err != nil {
		return fmt.Errorf("acp frame: %w", err)
	}
	return nil
}

func acpUsage(w io.Writer) {
	say(w, "usage: damnbot acp\n")
}
