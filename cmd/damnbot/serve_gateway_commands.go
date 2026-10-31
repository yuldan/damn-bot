package main

import (
	"cmp"
	"context"
	"errors"
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

// chatTurn is the cancel of one active turn. stopped and done are guarded by
// serveGateway.turnMu, so /stop and turn completion share one critical section.
type chatTurn struct {
	cancel  context.CancelFunc
	stopped bool
	done    bool
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
		g.submit(ctx, key, func(laneCtx context.Context) { g.handle(laneCtx, &update, false) })
		return
	}
	queued, drop, recorded, err := g.enqueueInbound(ctx, &update)
	switch {
	case err != nil:
		// A queue failure never drops the message: run the old in-memory path.
		sessionsPrintf(g.stderr, "gateway: queue %s: %s\n", cleanArg(update.UpdateID), cleanLine(err.Error()))
		g.metrics.IncError()
		if recorded {
			// The row is durable even though a pointer step failed; the
			// webhook may ack it and let the in-memory path run the turn.
			commitInbound(in, nil)
		}
		g.submit(ctx, key, func(laneCtx context.Context) { g.handle(laneCtx, &update, recorded) })
	case drop:
		commitInbound(in, nil) // replay or explicit overflow: the row owns the work
		return
	case queued:
		commitInbound(in, nil)
		g.submit(ctx, key, func(laneCtx context.Context) { g.drainChat(laneCtx, update.Platform, update.ChatID) })
	default:
		// Not allowlisted (or a weak identity): admission denies it without a
		// row, exactly as before REL-11.
		g.submit(ctx, key, func(laneCtx context.Context) { g.handle(laneCtx, &update, false) })
	}
}

// commitInbound reports the durable-queue outcome to a webhook transport. A
// nil callback is the poll path; the handler wraps it in a once so a second
// report from admission is harmless.
func commitInbound(in *telegram.Inbound, err error) {
	if in.Commit != nil {
		in.Commit(err)
	}
}

func (g *serveGateway) submit(ctx context.Context, key string, work func(context.Context)) {
	if err := g.chats.Submit(ctx, key, work); err != nil && ctx.Err() == nil {
		sessionsPrintf(g.stderr, "gateway: dispatch: %s\n", cleanLine(err.Error()))
		g.metrics.IncError()
	}
}

// replyCommand sends the one answer of a handled command. A command never
// reaches a turn, so this message is its whole output.
func (g *serveGateway) replyCommand(ctx context.Context, in *telegram.Inbound, text string) {
	defer g.restartAfterReply()
	if text == "" {
		return
	}
	if g.chatFlag(ctx, in.ChatID, chatTimestamps) {
		text = withTimestamp(text, timestampPrefix(g.now()))
	}
	key := gateway.DeliveryKey("telegram", in.ChatID, "cmd:"+in.UpdateID)
	if _, err := g.adapter.Send(ctx, &telegram.Outbound{ChatID: in.ChatID, ThreadID: in.ThreadID, Text: text, Key: key}); err != nil {
		g.metrics.IncError()
	}
}

// Timezone reads gateway_identities.tz; empty means the host zone (PROV-03).
func (g *serveGateway) Timezone(ctx context.Context, platform, externalID string) (string, error) {
	var tz string
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		gerr := tx.Get(`SELECT coalesce(tz, '') FROM gateway_identities WHERE platform = ?1 AND external_id = ?2`,
			[]any{platform, externalID}, &tz)
		if errors.Is(gerr, ctxdb.ErrNotFound) {
			return nil
		}
		if gerr != nil {
			return fmt.Errorf("read tz: %w", gerr)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("gateway: read tz: %w", err)
	}
	return tz, nil
}

// SetTimezone writes gateway_identities.tz; "" restores the host zone.
func (g *serveGateway) SetTimezone(ctx context.Context, platform, externalID, tz string) error {
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		n, xerr := tx.Exec(`UPDATE gateway_identities SET tz = ?3, updated_at = ?4
			WHERE platform = ?1 AND external_id = ?2`,
			platform, externalID, nullText(tz), time.Now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("update tz: %w", xerr)
		}
		if n != 1 {
			return fmt.Errorf("unknown identity %s:%s", platform, externalID)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gateway: set tz: %w", err)
	}
	return nil
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
func (g *serveGateway) beginTurn(ctx context.Context, scope *gateway.Scope) (context.Context, func(), bool) {
	turnCtx, cancel := context.WithCancel(ctx)
	key := turnKey{route: scope.Route(), principal: scope.Label()}
	turn := &chatTurn{cancel: cancel}
	g.turnMu.Lock()
	if old := g.turns[key]; old != nil && !old.done {
		// One active turn per (route, principal): a duplicate admission must
		// never replace the live entry, or /stop would claim a phantom turn.
		g.turnMu.Unlock()
		cancel()
		return turnCtx, func() {}, false
	}
	g.turns[key] = turn
	g.turnMu.Unlock()
	return turnCtx, func() {
		g.turnMu.Lock()
		turn.done = true
		if g.turns[key] == turn {
			delete(g.turns, key)
		}
		g.turnMu.Unlock()
		cancel()
	}, true
}

// StopTurn implements gateway.Runtime: cancel the caller's live turn in this
// chat. A turn already cancelled keeps its slot until the runner returns, so
// the second /stop reports no cancellation; another chat's turn is untouched.
func (g *serveGateway) StopTurn(_ context.Context, scope *gateway.Scope) (bool, error) {
	key := turnKey{route: scope.Route(), principal: scope.Label()}
	g.turnMu.Lock()
	defer g.turnMu.Unlock()
	turn := g.turns[key]
	if turn == nil || turn.stopped || turn.done {
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
// the version, the home channel and the emergency stop only.
func (g *serveGateway) Status(ctx context.Context) (gateway.RuntimeStatus, error) {
	queued, active := g.chats.Stats()
	est, err := g.estop.Status(ctx)
	paused, reason := est.Paused, est.Reason
	if err != nil {
		paused, reason = true, "state unreadable"
	}
	st := gateway.RuntimeStatus{
		Uptime:          g.now().Sub(g.started),
		QueueDepth:      queued + len(g.inbound),
		ActiveTurns:     active,
		SafeMode:        g.supervisor.Snapshot().State == heal.SafeMode,
		Paused:          paused,
		PauseReason:     reason,
		Version:         version,
		WaitingProvider: g.providerWait(),
	}
	if p, c, t, found, herr := g.store.HomeChannel(ctx); herr != nil {
		return gateway.RuntimeStatus{}, fmt.Errorf("gateway: status home: %w", herr)
	} else if found {
		st.Home = gateway.HomeName(p, c, t)
	}
	return st, nil
}

// TopicSession returns the forum thread's session, creating it when absent.
func (g *serveGateway) TopicSession(ctx context.Context, scope *gateway.Scope) (string, error) {
	return g.sessionFor(ctx, scope)
}

// TopicOff detaches the thread route and makes the whole chat use its chat
// session (topic_off), so later thread messages are routed without ThreadID.
func (g *serveGateway) TopicOff(ctx context.Context, scope *gateway.Scope) error {
	if err := g.store.SetChatSetting(ctx, scope.Platform, scope.ChatID, "topic_off", "true", g.now().UnixMilli()); err != nil {
		return fmt.Errorf("gateway: topic off: %w", err)
	}
	if err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`DELETE FROM gateway_conversations WHERE platform = ?1 AND chat_id = ?2 AND thread_id = ?3`,
			scope.Platform, scope.ChatID, scope.ThreadID)
		if xerr != nil {
			return fmt.Errorf("unbind topic: %w", xerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("gateway: topic off: %w", err)
	}
	return nil
}

// BindTopicSession binds an existing session to the forum thread.
func (g *serveGateway) BindTopicSession(ctx context.Context, scope *gateway.Scope, sessionID string) error {
	if _, err := g.store.DB.SessionInfo(ctx, sessionID); err != nil {
		return fmt.Errorf("gateway: topic bind: %w", err)
	}
	if err := g.store.SetChatSetting(ctx, scope.Platform, scope.ChatID, "topic_off", "false", g.now().UnixMilli()); err != nil {
		return fmt.Errorf("gateway: topic bind: %w", err)
	}
	if err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`DELETE FROM gateway_conversations WHERE platform = ?1 AND chat_id = ?2 AND thread_id = ?3`,
			scope.Platform, scope.ChatID, scope.ThreadID); xerr != nil {
			return fmt.Errorf("unbind topic: %w", xerr)
		}
		_, xerr := tx.Exec(`INSERT INTO gateway_conversations(platform, chat_id, thread_id, session_id, created_at)
			VALUES (?1, ?2, ?3, ?4, ?5)`,
			scope.Platform, scope.ChatID, scope.ThreadID, sessionID, g.now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("bind topic: %w", xerr)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("gateway: topic bind: %w", err)
	}
	return nil
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

// ResolveApproval answers a pending card from /approve or /deny.
func (g *serveGateway) ResolveApproval(_ context.Context, scope *gateway.Scope, id string, allow bool) (bool, error) {
	return g.adapter.DecideApproval(scope.ChatID, id, allow), nil
}

func (g *serveGateway) feedback(ctx context.Context, chat, thread, tool, preview string) (bool, error) {
	preview = cmp.Or(preview, tool)
	id, err := g.adapter.OpenApproval(ctx, chat, thread, preview)
	if err != nil {
		return false, fmt.Errorf("gateway: approval: %w", err)
	}
	if oerr := g.approvals.Open(&gateway.ApprovalRequest{
		ID: id, Platform: "telegram", ChatID: chat, UserID: chat,
		Summary: preview, ExpiresAt: time.Now().Add(5 * time.Minute),
	}); oerr != nil {
		return false, fmt.Errorf("gateway: approval registry: %w", oerr)
	}
	d, err := g.adapter.AwaitApproval(ctx, chat, id)
	if err != nil {
		return false, fmt.Errorf("gateway: approval: %w", err)
	}
	allow := d == telegram.DecisionAllow
	decision := gateway.Deny
	if allow {
		decision = gateway.AllowOnce
	}
	if _, derr := g.approvals.Decide("telegram", chat, id, decision); derr != nil {
		return false, fmt.Errorf("gateway: approval decision: %w", derr)
	}
	return allow, nil
}
