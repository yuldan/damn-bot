package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/heal"
)

// turnKey identifies one active turn: the session route plus the gateway
// principal whose session it runs on (SPEC §9.7 keeps a session per route and
// a label per principal).
type turnKey struct {
	route     gateway.Route
	principal string
}

// chatTurn is the cancel of one active turn and whether /stop already used it.
type chatTurn struct {
	cancel  context.CancelFunc
	stopped bool
}

// dispatch queues one update on its chat lane. Command messages go to a
// parallel per-chat command lane, so /stop, /new, /status and /usage are
// admitted while the chat's turn is still occupying the turn lane; ordinary
// text keeps strict per-chat FIFO order.
func (g *serveGateway) dispatch(ctx context.Context, in *telegram.Inbound) {
	update := *in
	key := update.Platform + "\x00" + update.ChatID
	if _, ok := gateway.Parse(update.Text); ok {
		key += "\x00cmd"
	}
	if err := g.chats.Submit(ctx, key, func(laneCtx context.Context) { g.handle(laneCtx, &update) }); err != nil && ctx.Err() == nil {
		sessionsPrintf(g.stderr, "gateway: dispatch %s: %s\n", cleanArg(update.UpdateID), cleanLine(err.Error()))
		g.metrics.IncError()
	}
}

// replyCommand sends the one answer of a handled command. A command never
// reaches a turn, so this message is its whole output.
func (g *serveGateway) replyCommand(ctx context.Context, in *telegram.Inbound, text string) {
	if text == "" {
		return
	}
	if g.chatFlag(ctx, "telegram", in.ChatID, chatTimestamps) {
		now := g.now
		if now == nil {
			now = time.Now
		}
		text = withTimestamp(text, timestampPrefix(now()))
	}
	key := gateway.DeliveryKey("telegram", in.ChatID, "cmd:"+in.UpdateID)
	if _, err := g.adapter.Send(ctx, &telegram.Outbound{ChatID: in.ChatID, ThreadID: in.ThreadID, Text: text, Key: key}); err != nil {
		g.metrics.IncError()
	}
}

// gatewayUsers mirrors [gateway.users.<id>].workspace, keyed "<platform>:<identity>"
// as internal/config validates it.
func gatewayUsers(cfg *config.Config) map[string]string {
	users := make(map[string]string, len(cfg.Gateway.Users))
	for id, u := range cfg.Gateway.Users {
		users[id] = u.Workspace
	}
	return users
}

// seedGatewayIdentities inserts the env bootstrap rows. EnsureIdentity never
// overwrites an existing row, so the DB stays the source of truth.
func seedGatewayIdentities(ctx context.Context, store gwstore.Store, owner string, allow []string) error {
	for i, spec := range append([]string{owner}, allow...) {
		role := "user"
		if i == 0 {
			role = "owner"
		}
		platform, id, _ := strings.Cut(spec, ":")
		now := time.Now().UnixMilli()
		if _, err := store.EnsureIdentity(ctx, platform, id, role, "platform_authenticated", now); err != nil {
			return fmt.Errorf("gateway: seed %s: %w", role, err)
		}
		if err := store.SetStatus(ctx, platform, id, "allowlisted", now); err != nil {
			return fmt.Errorf("gateway: seed %s status: %w", role, err)
		}
	}
	return nil
}

// createSession stores one new session and binds it to scope's route for
// scope's principal. It never deletes an earlier binding: gwstore.Session reads
// the newest row, so /new starts fresh while the previous session stays.
func (g *serveGateway) createSession(ctx context.Context, scope *gateway.Scope) (string, error) {
	sid, err := newSessionID()
	if err != nil {
		return "", err
	}
	cwd := scope.Workspace
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return "", fmt.Errorf("gateway: getwd: %w", err)
		}
	}
	if _, cerr := g.store.DB.CreateSession(ctx, ctxdb.Session{
		ID: sid, Origin: "gateway", Cwd: cwd, Model: g.modelRef,
		ConfigSHA: hashConfig(&g.cfg), GatewayIdentity: scope.Label(),
	}); cerr != nil {
		return "", fmt.Errorf("gateway: create session: %w", cerr)
	}
	route := scope.Route()
	err = g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_conversations(platform, chat_id, thread_id, session_id, created_at)
			VALUES (?1, ?2, ?3, ?4, ?5) ON CONFLICT DO NOTHING`,
			route.Platform, route.ChatID, route.ThreadID, sid, g.now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("bind route: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("gateway: bind session: %w", err)
	}
	return sid, nil
}

// beginTurn registers the turn's cancel so /stop can reach it, and returns the
// per-chat context the runner (and the loop inside it) already honours.
func (g *serveGateway) beginTurn(ctx context.Context, scope *gateway.Scope) (context.Context, func()) {
	turnCtx, cancel := context.WithCancel(ctx)
	key := turnKey{route: scope.Route(), principal: scope.Label()}
	turn := &chatTurn{cancel: cancel}
	g.turnMu.Lock()
	g.turns[key] = turn
	g.turnMu.Unlock()
	return turnCtx, func() {
		g.turnMu.Lock()
		if g.turns[key] == turn {
			delete(g.turns, key)
		}
		g.turnMu.Unlock()
		cancel()
	}
}

// StopTurn implements gateway.Runtime: cancel the caller's live turn in this
// chat. A turn already cancelled keeps its slot until the runner returns, so
// the second /stop reports no cancellation; another chat's turn is untouched.
func (g *serveGateway) StopTurn(_ context.Context, scope *gateway.Scope) (bool, error) {
	key := turnKey{route: scope.Route(), principal: scope.Label()}
	g.turnMu.Lock()
	defer g.turnMu.Unlock()
	turn := g.turns[key]
	if turn == nil || turn.stopped {
		return false, nil
	}
	turn.stopped = true
	turn.cancel()
	return true, nil
}

// NewSession implements gateway.Runtime: bind a fresh session to the caller's
// route; the previous session is kept.
func (g *serveGateway) NewSession(ctx context.Context, scope *gateway.Scope) (string, error) {
	return g.createSession(ctx, scope)
}

// Status implements gateway.Runtime. It reads the chat queue, the supervisor,
// the version and the home channel only; it never starts work.
func (g *serveGateway) Status(ctx context.Context) (gateway.RuntimeStatus, error) {
	queued, active := g.chats.Stats()
	st := gateway.RuntimeStatus{
		Uptime:      g.now().Sub(g.started),
		QueueDepth:  queued + len(g.inbound),
		ActiveTurns: active,
		SafeMode:    g.supervisor.Snapshot().State == heal.SafeMode,
		Version:     version,
	}
	if p, c, t, found, err := g.store.HomeChannel(ctx); err != nil {
		return gateway.RuntimeStatus{}, fmt.Errorf("gateway: status home: %w", err)
	} else if found {
		st.Home = gateway.HomeName(p, c, t)
	}
	return st, nil
}

// TopicSession implements gateway.Runtime: the forum thread's session, created
// on first use. The command layer has already rejected an empty thread.
func (g *serveGateway) TopicSession(ctx context.Context, scope *gateway.Scope) (string, error) {
	return g.sessionFor(ctx, scope)
}

// Usage implements gateway.Runtime over the existing usage ledger: the session
// bound to the caller's route and the current day, both read-only. ok is false
// when the session has no ledger row, never a fabricated zero.
func (g *serveGateway) Usage(ctx context.Context, scope *gateway.Scope) (gateway.UsageReport, bool, error) {
	route := gwstore.Route{Platform: scope.Platform, ChatID: scope.ChatID, ThreadID: scope.ThreadID}
	sid, found, err := g.store.Session(ctx, route, scope.Label())
	if err != nil {
		return gateway.UsageReport{}, false, fmt.Errorf("gateway: usage session: %w", err)
	}
	if !found {
		return gateway.UsageReport{}, false, nil
	}
	totals, err := ledgerstore.SessionTotals(ctx, g.store.DB, sid)
	if err != nil {
		return gateway.UsageReport{}, false, fmt.Errorf("gateway: usage totals: %w", err)
	}
	if totals.Calls == 0 {
		return gateway.UsageReport{}, false, nil
	}
	now := g.now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	dayCost, dayUnknown, err := ledgerstore.WindowTotals(ctx, g.store.DB, day.UnixMilli(), day.AddDate(0, 0, 1).UnixMilli(), "")
	if err != nil {
		return gateway.UsageReport{}, false, fmt.Errorf("gateway: usage day: %w", err)
	}
	return gateway.UsageReport{
		SessionIn: totals.Input, SessionOut: totals.Output,
		SessionCostMicros: totals.CostMicros, SessionCostKnown: totals.UnknownCost == 0,
		DayCostMicros: dayCost, DayCostKnown: dayUnknown == 0,
	}, true, nil
}
