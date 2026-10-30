package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// chatReply is the serve side of one chat request: an append-only JSONL file
// the CLI tails. Every write is a complete line, so the reader never has to
// retract a partial answer.
type chatReply struct {
	mu       sync.Mutex
	f        *os.File
	err      error
	finished bool
}

func newChatReply(dataDir, id string) (*chatReply, error) {
	dir := filepath.Join(dataDir, "run", "chat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("gateway: chat reply dir: %w", err)
	}
	f, err := os.OpenFile(chatReplyPath(dataDir, id), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("gateway: chat reply file: %w", err)
	}
	return &chatReply{f: f}, nil
}

// emit appends one protocol line. Terminal kinds close the request for the
// reader; a write error is kept and reported when the turn ends.
func (r *chatReply) emit(kind, text, reason string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	b, err := json.Marshal(chatReplyLine{Kind: kind, Text: text, Reason: reason})
	if err != nil {
		r.err = err
		return
	}
	if _, err := r.f.Write(append(b, '\n')); err != nil {
		r.err = err
		return
	}
	if kind == "done" || kind == "error" || kind == "rejected" {
		r.finished = true
	}
}

func (r *chatReply) terminal() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

func (r *chatReply) close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return r.err
	}
	cerr := r.f.Close()
	r.f = nil
	return errors.Join(r.err, cerr)
}

// stream drains a turn into delta/notice lines and one terminal line. It must
// drain the channel to close even when writing fails: the loop owns the send
// side, so returning early would leak its goroutine.
func (r *chatReply) stream(ch <-chan event.Update) error {
	var pending []byte
	var stop event.StopReason
	var detail event.StopDetail
	var turnErr string
	for up := range ch {
		switch up.Kind {
		case event.UpdateTextDelta:
			pending = append(pending, up.Text...)
			if len(pending) >= chatDeltaMin {
				r.emit("delta", string(pending), "")
				pending = pending[:0]
			}
		case event.UpdateNotice:
			r.emit("notice", up.Text, "")
		case event.UpdateStop:
			stop, detail, turnErr = up.Stop, up.Detail, up.Err
		case event.UpdateToolCallStart, event.UpdateToolCallArgs, event.UpdateToolCallEnd,
			event.UpdateToolResult, event.UpdateUsage:
		}
	}
	if len(pending) > 0 {
		r.emit("delta", string(pending), "")
	}
	switch {
	case stop == event.StopError || detail == event.DetailProviderError:
		r.emit("error", "damnbot: provider error ("+string(detail)+")", "")
	case turnErr != "":
		r.emit("error", cleanLine(turnErr), "")
	default:
		r.emit("done", "", "")
	}
	return r.err
}

// startChat applies one chat request from the spool. Validation happens before
// any work; a malformed request still gets a terminal error line when its id
// is usable, so the CLI never waits for an answer that cannot come.
func (g *serveGateway) startChat(ctx context.Context, req *sendSpoolRequest) error {
	if !validChatRequestID(req.ID) {
		return errors.New("chat: invalid request id")
	}
	reply, err := newChatReply(g.dataDir, req.ID)
	if err != nil {
		return err
	}
	if err := validateChatRequest(req); err != nil {
		reply.emit("error", cleanLine(err.Error()), "")
		return errors.Join(err, reply.close())
	}
	if g.chats == nil {
		reply.emit("error", "gateway: chat queue is unavailable", "")
		return errors.Join(errors.New("gateway: chat queue is unavailable"), reply.close())
	}
	key := req.Platform + "\x00" + req.Chat
	if serr := g.chats.Submit(ctx, key, func(laneCtx context.Context) {
		g.serveChat(laneCtx, req, reply)
	}); serr != nil {
		reply.emit("error", cleanLine(serr.Error()), "")
		return errors.Join(fmt.Errorf("gateway: chat %s: %w", req.ID, serr), reply.close())
	}
	return nil
}

func validateChatRequest(req *sendSpoolRequest) error {
	for _, f := range [...]struct{ name, value string }{
		{"platform", req.Platform},
		{"chat", req.Chat},
		{"sender", req.Sender},
	} {
		if f.value == "" || len(f.value) > 512 || strings.ContainsAny(f.value, "\x00\r\n") {
			return fmt.Errorf("chat: %s is required and must be a single line", f.name)
		}
	}
	if len(req.Thread) > 512 || strings.ContainsAny(req.Thread, "\x00\r\n") {
		return errors.New("chat: thread must be a single line")
	}
	if req.Text == "" || len(req.Text) > 32*1024 || strings.ContainsRune(req.Text, 0) {
		return errors.New("chat: text is required and bounded")
	}
	return nil
}

// serveChat runs one chat request through the same gate and turn loop as a
// platform update. Only the reply sink differs: it writes the answer instead of
// calling a platform adapter.
func (g *serveGateway) serveChat(ctx context.Context, req *sendSpoolRequest, reply *chatReply) {
	defer func() {
		if err := reply.close(); err != nil {
			sessionsPrintf(g.stderr, "gateway: chat %s: %s\n", cleanArg(req.ID), cleanLine(err.Error()))
		}
	}()
	in := &telegram.Inbound{
		Platform: req.Platform, ChatID: req.Chat, ThreadID: req.Thread,
		SenderID: req.Sender, UpdateID: "cli:" + req.ID, Text: req.Text,
	}
	inbound := &gateway.Inbound{
		Platform: in.Platform, ChatID: in.ChatID, ThreadID: in.ThreadID, SenderID: in.SenderID,
		UpdateID: in.UpdateID, Group: strings.HasPrefix(in.ChatID, "-"), Text: in.Text,
		Payload: `{"kind":"text","text":` + strconv.Quote(in.Text) + `}`,
	}
	out, err := g.gate.Admit(ctx, inbound, func(wctx context.Context, t gateway.Ticket) error {
		reply.emit("status", "accepted", "")
		turnCtx, done, ok := g.beginTurn(wctx, &t.Scope)
		if !ok {
			return errors.New("gateway: turn already active")
		}
		defer done()
		return g.turnLocal(turnCtx, in, &t, reply)
	})
	switch {
	case err != nil:
		if !reply.terminal() {
			reply.emit("error", cleanLine(err.Error()), "")
		}
		sessionsPrintf(g.stderr, "gateway: chat %s: %s\n", cleanArg(req.ID), cleanLine(err.Error()))
		g.metrics.IncError()
	case out.Kind == gateway.Rejected:
		reply.emit("rejected", "", string(out.Reason))
		g.metrics.IncError()
	case out.Kind == gateway.CommandHandled:
		if out.Reply != "" {
			reply.emit("delta", out.Reply, "")
		}
		reply.emit("done", "", "")
	case out.Kind == gateway.Duplicate:
		reply.emit("error", "duplicate request", "")
	default:
		if !reply.terminal() {
			reply.emit("done", "", "")
		}
	}
}

// turnLocal is turn() for the CLI sink: the same quota, session and runChat
// path, without a typing indicator or a platform placeholder. Tool approvals
// stay denied because runChat leaves the approvers nil for this mode.
func (g *serveGateway) turnLocal(ctx context.Context, in *telegram.Inbound, t *gateway.Ticket, reply *chatReply) error {
	req := gateway.QuotaRequest{
		Platform: t.Scope.Platform, Identity: t.Scope.UserID,
		ChatID: t.Scope.ChatID, Group: strings.HasPrefix(t.Scope.ChatID, "-"),
	}
	res, err := g.quota.Take(req)
	if err != nil {
		return fmt.Errorf("gateway: quota: %w", err)
	}
	if res.Limit != gateway.LimitNone {
		g.metrics.IncError()
		reply.emit("notice", res.Message(), "")
		return nil
	}
	_, end, ok := g.daemon.BeginTurn()
	if !ok {
		return errors.New("gateway: daemon is draining")
	}
	defer end()
	sid, err := g.sessionFor(ctx, &t.Scope)
	if err != nil {
		return err
	}
	return g.runChat(ctx, in, sid, func(ctx context.Context, ch <-chan event.Update) error {
		return reply.stream(ch)
	}, true)
}
