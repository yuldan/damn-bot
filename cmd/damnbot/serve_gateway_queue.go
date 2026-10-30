package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
)

// Inbound queue (REL-11). The durable source of truth is gateway_inbound with
// its payload; module_state holds only the per-chat FIFO pointers and the chat
// lease. The in-memory ChatQueue is the cache that drains them.
const (
	inboundQueueLimit = 50
	chatLeaseTTL      = 30 * time.Second
	queueModule       = "gateway"
)

// queueKey is one FIFO pointer: "q:<platform>:<chat>:<nanos>". UpdateID lives in
// the value so an id containing ':' never confuses the key layout.
func queueKey(platform, chat string) string {
	return "q:" + platform + ":" + chat + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func queuePrefix(platform, chat string) string { return "q:" + platform + ":" + chat + ":" }

// enqueueInbound commits one allowlisted update to the durable queue. queued
// means a lane must drain it; drop means it was a replay or an overflow that
// was already answered. Not allowlisted/weak updates return neither and are
// left to admission, which denies them without writing a row.
func (g *serveGateway) enqueueInbound(ctx context.Context, in *telegram.Inbound) (queued, drop, recorded bool, err error) {
	id, found, err := g.store.Lookup(ctx, in.Platform, in.SenderID)
	if err != nil {
		return false, false, false, fmt.Errorf("gateway: queue lookup: %w", err)
	}
	if !found || id.Status != "allowlisted" || id.Strength == "weak" {
		return false, false, false, nil
	}
	payload := `{"kind":"text","text":` + strconv.Quote(in.Text) + `}`
	inserted, err := g.store.Record(ctx, &gwstore.Inbound{
		Platform: in.Platform, ChatID: in.ChatID, ThreadID: in.ThreadID,
		SenderID: in.SenderID, UpdateID: in.UpdateID, Payload: payload,
	})
	if err != nil {
		return false, false, false, fmt.Errorf("gateway: queue record: %w", err)
	}
	if !inserted {
		return false, true, true, nil // replay: the original row owns the work
	}
	depth, err := g.queueDepth(ctx, in.Platform, in.ChatID)
	if err != nil {
		return false, false, true, err
	}
	if g.busyDrop(ctx, in, depth) {
		return false, true, true, nil
	}
	value, merr := json.Marshal(map[string]string{"update_id": in.UpdateID})
	if merr != nil {
		return false, false, true, fmt.Errorf("gateway: queue encode: %w", merr)
	}
	err = g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at)
			VALUES (?1, ?2, ?3, ?4) ON CONFLICT(module_id, key) DO NOTHING`,
			queueModule, queueKey(in.Platform, in.ChatID), string(value), time.Now().UnixMilli()); xerr != nil {
			return fmt.Errorf("queue insert: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return false, false, true, fmt.Errorf("gateway: queue insert: %w", err)
	}
	return true, false, true, nil
}

// busyDrop applies gateway.busy_policy: reject_notify refuses while busy,
// queue accepts to the bounded limit, and steer advertises its missing seam
// once and then queues.
func (g *serveGateway) busyDrop(ctx context.Context, in *telegram.Inbound, depth int) bool {
	policy := g.cfg.Gateway.BusyPolicy
	if policy == "" {
		policy = "queue"
	}
	if depth > 0 && policy == "reject_notify" {
		g.notifyChat(ctx, in.Platform, in.ChatID, "sys:busy:"+in.UpdateID,
			fmt.Sprintf("sibuk (busy_policy=reject_notify, antre %d/%d), kirim ulang setelah turn selesai", depth, inboundQueueLimit))
		return true
	}
	if depth >= inboundQueueLimit {
		g.notifyChat(ctx, in.Platform, in.ChatID, "sys:busy:"+in.UpdateID,
			fmt.Sprintf("antre %d/%d (penuh), kirim ulang setelah antrean turun", depth, inboundQueueLimit))
		return true
	}
	if depth > 0 && policy == "steer" {
		g.notifyChat(ctx, in.Platform, in.ChatID, "sys:steer:"+in.UpdateID,
			fmt.Sprintf("antre %d/%d (steer: belum ada seam injeksi, pesan diantre)", depth+1, inboundQueueLimit))
	}
	return false
}

// queueDepth counts the FIFO pointers of one chat.
func (g *serveGateway) queueDepth(ctx context.Context, platform, chat string) (int, error) {
	var n int
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if xerr := tx.Get(`SELECT count(*) FROM module_state WHERE module_id = ?1 AND key LIKE ?2`,
			[]any{queueModule, queuePrefix(platform, chat) + "%"}, &n); xerr != nil {
			return fmt.Errorf("queue depth: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("gateway: queue depth: %w", err)
	}
	return n, nil
}

// nextQueued returns the oldest pointer of one chat without removing it. The
// pointer is deleted only after the turn returns, so a crash mid-turn leaves
// the update in the durable queue; admission dedupes a replay.
func (g *serveGateway) nextQueued(ctx context.Context, platform, chat string) (key string, in *telegram.Inbound, ok bool, err error) {
	var raw string
	err = g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		gerr := tx.Get(`SELECT key || char(31) || value FROM module_state
			WHERE module_id = ?1 AND key LIKE ?2 ORDER BY key LIMIT 1`,
			[]any{queueModule, queuePrefix(platform, chat) + "%"}, &raw)
		if gerr == nil {
			return nil
		}
		if isNotFound(gerr) {
			return nil
		}
		return fmt.Errorf("queue head: %w", gerr)
	})
	if err != nil {
		return "", nil, false, fmt.Errorf("gateway: queue head: %w", err)
	}
	if raw == "" {
		return "", nil, false, nil
	}
	key, value, _ := strings.Cut(raw, "\x1f")
	var meta struct {
		UpdateID string `json:"update_id"`
	}
	if jerr := json.Unmarshal([]byte(value), &meta); jerr != nil || meta.UpdateID == "" {
		if cerr := g.completeQueued(ctx, key); cerr != nil {
			return "", nil, false, cerr
		}
		return "", nil, false, fmt.Errorf("gateway: queue pointer %s: %w", key, jerr)
	}
	loaded, found, lerr := g.loadQueuedInbound(ctx, platform, meta.UpdateID)
	if lerr != nil {
		return "", nil, false, lerr
	}
	if !found {
		if cerr := g.completeQueued(ctx, key); cerr != nil {
			return "", nil, false, cerr
		}
		return "", nil, false, nil
	}
	return key, loaded, true, nil
}

// loadQueuedInbound reopens one gateway_inbound row, so the queue never keeps
// a second copy of the message text (the row is sealed at rest).
func (g *serveGateway) loadQueuedInbound(ctx context.Context, platform, updateID string) (*telegram.Inbound, bool, error) {
	var (
		in      telegram.Inbound
		payload string
		sealed  []byte
		raw     string
	)
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if xerr := tx.Get(`SELECT chat_id, thread_id, sender_id, coalesce(payload, '{}'), coalesce(payload_enc, x'')
			FROM gateway_inbound WHERE platform = ?1 AND external_id = ?2`,
			[]any{platform, updateID}, &in.ChatID, &in.ThreadID, &in.SenderID, &payload, &sealed); xerr != nil {
			return fmt.Errorf("queue row: %w", xerr)
		}
		opened, xerr := tx.OpenRow("gateway_inbound.payload", platform+":"+updateID, sealed, payload)
		if xerr != nil {
			return fmt.Errorf("queue open payload: %w", xerr)
		}
		raw = opened
		return nil
	})
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("gateway: queue load: %w", err)
	}
	var content struct {
		Text string `json:"text"`
	}
	if jerr := json.Unmarshal([]byte(raw), &content); jerr != nil {
		return nil, false, fmt.Errorf("gateway: queue payload json: %w", jerr)
	}
	in.Platform, in.UpdateID, in.Text = platform, updateID, content.Text
	in.Group = strings.HasPrefix(in.ChatID, "-")
	return &in, true, nil
}

func (g *serveGateway) completeQueued(ctx context.Context, key string) error {
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`DELETE FROM module_state WHERE module_id = ?1 AND key = ?2`, queueModule, key); xerr != nil {
			return fmt.Errorf("queue delete: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gateway: queue complete: %w", err)
	}
	return nil
}

// drainChat claims the chat lease and drains its durable FIFO. A lease held by
// a live process leaves the rows in place and answers one `__system__` notice.
func (g *serveGateway) drainChat(ctx context.Context, platform, chat string) {
	ok, err := g.acquireChatLease(ctx, platform, chat)
	if err != nil {
		sessionsPrintf(g.stderr, "gateway: chat lease %s: %s\n", cleanArg(chat), cleanLine(err.Error()))
		return
	}
	if !ok {
		g.notifyChat(ctx, platform, chat, "sys:lease:"+platform+":"+chat,
			"sesi chat ini sedang diproses proses lain; pesan tetap diantre")
		return
	}
	defer g.releaseChatLease(context.WithoutCancel(ctx), platform, chat)
	for {
		key, in, found, err := g.nextQueued(ctx, platform, chat)
		if err != nil {
			sessionsPrintf(g.stderr, "gateway: queue next %s: %s\n", cleanArg(chat), cleanLine(err.Error()))
			return
		}
		if !found {
			return
		}
		if err := g.markQueuedStarted(ctx, key); err != nil {
			sessionsPrintf(g.stderr, "gateway: queue start %s: %s\n", cleanArg(chat), cleanLine(err.Error()))
			return
		}
		g.handle(ctx, in, true)
		if err := g.completeQueued(ctx, key); err != nil {
			sessionsPrintf(g.stderr, "gateway: queue complete %s: %s\n", cleanArg(chat), cleanLine(err.Error()))
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// resumeInbound submits one drain per chat that still has durable rows; Start
// calls it so a restart continues the queue instead of losing it.
func (g *serveGateway) resumeInbound(ctx context.Context) {
	type chatKey struct{ platform, chat string }
	seen := map[chatKey]bool{}
	var keys []string
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		serr := error(nil)
		keys, serr = tx.Strings(`SELECT key FROM module_state WHERE module_id = ?1 AND key LIKE 'q:%' ORDER BY key`, queueModule)
		if serr != nil {
			return fmt.Errorf("resume queue select: %w", serr)
		}
		return nil
	})
	if err != nil {
		sessionsPrintf(g.stderr, "gateway: resume queue: %s\n", cleanLine(err.Error()))
		return
	}
	for _, key := range keys {
		started, serr := g.queuedStarted(ctx, key)
		if serr != nil {
			sessionsPrintf(g.stderr, "gateway: resume check %s: %s\n", cleanArg(key), cleanLine(serr.Error()))
			continue
		}
		if started {
			// The previous process died after this update was admitted: the
			// turn recovery in ctxdb closes it crashed, so replaying would
			// duplicate effects and block newer queued messages.
			if cerr := g.completeQueued(ctx, key); cerr != nil {
				sessionsPrintf(g.stderr, "gateway: drop started %s: %s\n", cleanArg(key), cleanLine(cerr.Error()))
			}
			continue
		}
		parts := strings.SplitN(key, ":", 4)
		if len(parts) != 4 {
			continue
		}
		ck := chatKey{platform: parts[1], chat: parts[2]}
		if seen[ck] {
			continue
		}
		seen[ck] = true
		key := ck
		if err := g.chats.Submit(ctx, key.platform+"\x00"+key.chat, func(laneCtx context.Context) {
			g.drainChat(laneCtx, key.platform, key.chat)
		}); err != nil && ctx.Err() == nil {
			sessionsPrintf(g.stderr, "gateway: resume queue %s: %s\n", cleanArg(key.chat), cleanLine(err.Error()))
		}
	}
}

// acquireChatLease takes (or renews) the per-chat lease in module_state. An
// expired lease is taken over; a live foreign lease refuses.
func (g *serveGateway) acquireChatLease(ctx context.Context, platform, chat string) (bool, error) {
	owner := g.leaseOwner()
	now := time.Now()
	until := now.Add(chatLeaseTTL).UnixMilli()
	ok := false
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var raw string
		gerr := tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{queueModule, "lease:" + platform + ":" + chat}, &raw)
		if gerr == nil {
			var st struct {
				Owner string `json:"owner"`
				Until int64  `json:"until"`
			}
			if json.Unmarshal([]byte(raw), &st) == nil && st.Owner != owner && st.Until > now.UnixMilli() {
				return nil
			}
		} else if !isNotFound(gerr) {
			return fmt.Errorf("lease read: %w", gerr)
		}
		value, merr := json.Marshal(map[string]any{"owner": owner, "until": until})
		if merr != nil {
			return fmt.Errorf("lease encode: %w", merr)
		}
		_, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES (?1, ?2, ?3, ?4)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			queueModule, "lease:"+platform+":"+chat, string(value), now.UnixMilli())
		if xerr != nil {
			return fmt.Errorf("lease write: %w", xerr)
		}
		ok = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("gateway: chat lease: %w", err)
	}
	return ok, nil
}

func (g *serveGateway) releaseChatLease(ctx context.Context, platform, chat string) {
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`DELETE FROM module_state WHERE module_id = ?1 AND key = ?2`,
			queueModule, "lease:"+platform+":"+chat); xerr != nil {
			return fmt.Errorf("lease delete: %w", xerr)
		}
		return nil
	})
	if err != nil {
		sessionsPrintf(g.stderr, "gateway: release lease %s: %s\n", cleanArg(chat), cleanLine(err.Error()))
	}
}

// markQueuedStarted records that a pointer was handed to a turn. A restart
// treats it as crashed (no replay) so it cannot block newer updates.
func (g *serveGateway) markQueuedStarted(ctx context.Context, key string) error {
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		var raw string
		if err := tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{queueModule, key}, &raw); err != nil {
			return fmt.Errorf("queue start read: %w", err)
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(raw), &meta); err != nil {
			return fmt.Errorf("queue start json: %w", err)
		}
		meta["started"] = true
		value, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("queue start encode: %w", err)
		}
		if _, err := tx.Exec(`UPDATE module_state SET value = ?3, updated_at = ?4 WHERE module_id = ?1 AND key = ?2`,
			queueModule, key, string(value), time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("queue start write: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gateway: mark queued %s: %w", cleanArg(key), err)
	}
	return nil
}

func (g *serveGateway) queuedStarted(ctx context.Context, key string) (bool, error) {
	var raw string
	err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		if err := tx.Get(`SELECT value FROM module_state WHERE module_id = ?1 AND key = ?2`,
			[]any{queueModule, key}, &raw); err != nil {
			return fmt.Errorf("queue state read: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("gateway: queued state %s: %w", cleanArg(key), err)
	}
	var meta struct {
		Started bool `json:"started"`
	}
	return json.Unmarshal([]byte(raw), &meta) == nil && meta.Started, nil
}

// leaseOwner is the data dir: serve holds its flock, so a lease from the same
// directory is this process (or its predecessor) and never blocks the queue.
func (g *serveGateway) leaseOwner() string { return g.dataDir }
