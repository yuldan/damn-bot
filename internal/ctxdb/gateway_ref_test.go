// Test-only REFERENCE implementation (ADR-053, TS-P09). The gateway protocol logic
// (poller/worker/spool/outbox) belongs in package internal/gateway (F4); ctxdb only
// exposes storage primitives (tables + transactional methods) behind consumer-side
// interfaces (ENGINEERING E-03/E-09). Kept here as the oracle of scenario_gateway_test.go until F4.

package ctxdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Inbound is one normalised, redacted inbound message (SELF-HEALING §5.1).
type Inbound struct {
	Platform   string         `json:"platform"`
	ExternalID string         `json:"external_id"`
	ChatID     string         `json:"chat_id"`
	SenderID   string         `json:"sender_id"`
	Payload    string         `json:"payload"` // JSON {text, attachments[].blob_sha, kind}
	Pending    []*PendingBlob `json:"-"`
}

// IngestInbound is the poller/spool transaction. Dedupe uses
// ON CONFLICT(platform, external_id) DO NOTHING instead of INSERT OR IGNORE:
// OR IGNORE also swallows CHECK/NOT NULL, losing an invalid payload silently
// while the offset advances (AF-01). Here such a row fails the whole
// transaction (inbound_invalid) so the offset never advances past it.
func (s *Store) IngestInbound(ctx context.Context, msgs []Inbound, offsetKey, offset string) error {
	return s.Write(ctx, func(t *Tx) error {
		for i := range msgs {
			if err := t.ingestOne(&msgs[i]); err != nil {
				return err
			}
		}
		if offsetKey == "" {
			return nil
		}
		_, err := t.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('gateway', ?, ?, ?)
			ON CONFLICT(module_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, offsetKey, offset, t.s.clk.WallMs())
		return err
	})
}

// ingestOne publishes the attachments and inserts one inbound row (+ refs).
func (t *Tx) ingestOne(m *Inbound) error {
	for _, p := range m.Pending {
		if err := t.Publish(p); err != nil {
			return err
		}
	}
	ids, err := t.Strings(`INSERT INTO gateway_inbound(platform, external_id, chat_id, sender_id, payload, received_at)
		VALUES (?,?,?,?,?,?) ON CONFLICT(platform, external_id) DO NOTHING RETURNING external_id`,
		m.Platform, m.ExternalID, m.ChatID, m.SenderID, nullable(m.Payload), t.s.clk.WallMs())
	if err != nil {
		return fmt.Errorf("ctxdb: inbound %s:%s: %w: %w", m.Platform, m.ExternalID, ErrInboundInvalid, err)
	}
	if len(ids) == 0 {
		return nil // duplicate: original row and content kept
	}
	refs, err := BlobRefsOf("gateway_inbound", m.Platform+":"+m.ExternalID, nil, map[string]string{"payload": m.Payload})
	if err != nil {
		return err
	}
	return t.InsertRefs(SystemSession, t.s.clk.WallMs(), refs)
}

// Processed is the worker outcome for one queued inbound row.
type Processed struct {
	Session, Turn string
	EventID       int64
	NewSession    bool
}

type inboundPayload struct {
	Text        string `json:"text"`
	Attachments []struct {
		BlobSha string `json:"blob_sha"`
	} `json:"attachments"`
}

// decodeInbound returns the text and attachment shas of a queued payload.
func decodeInbound(payload string) (string, []string, error) {
	var p inboundPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", nil, fmt.Errorf("ctxdb: inbound payload: %w: %w", ErrInboundInvalid, err)
	}
	att := make([]string, 0, len(p.Attachments))
	for _, a := range p.Attachments {
		att = append(att, a.BlobSha)
	}
	return p.Text, att, nil
}

// ProcessInbound is the worker transaction for one chat: oldest queued row
// (insertion order — rowid — not received_at/external_id, AF-03) becomes a
// user_message in the chat session, and turn_start + journal `running` are
// written in the same transaction (AF-02) so no committed message is left
// without a turn. A conversation whose session is purging is treated as
// absent: new session + one system notice per chat per txid.
func (s *Store) ProcessInbound(ctx context.Context, platform, chat string) (Processed, error) {
	var out Processed
	busy := false
	err := s.Write(ctx, func(t *Tx) error {
		out, busy = Processed{}, false
		var ext, payload string
		if err := t.Get(`SELECT external_id, payload FROM gateway_inbound WHERE platform = ? AND chat_id = ? AND event_id IS NULL
			ORDER BY rowid LIMIT 1`, []any{platform, chat}, &ext, &payload); err != nil {
			return err
		}
		sid, isNew, err := t.chatSession(platform, chat)
		if err != nil {
			return err
		}
		if busy, err = t.leaseChat(platform, chat, sid, isNew); busy || err != nil {
			return err
		}
		text, att, err := decodeInbound(payload)
		if err != nil {
			return err
		}
		out.Session, out.NewSession, out.Turn = sid, isNew, newID()
		if out.EventID, err = t.AppendEvent(sid, &Event{
			Kind: "user_message", Role: "user", Content: text, Attach: att,
			Payload: jsonOf(map[string]string{"inbound": platform + ":" + ext}),
		}); err != nil {
			return err
		}
		n, err := t.Exec("UPDATE gateway_inbound SET event_id = ?, payload = NULL WHERE platform = ? AND external_id = ? AND event_id IS NULL",
			out.EventID, platform, ext)
		if err == nil && n == 0 {
			err = fmt.Errorf("ctxdb: inbound already processed: %w", ErrNotFound)
		}
		if err != nil {
			return err
		}
		return t.StartTurn(sid, out.Turn)
	})
	if err == nil && busy {
		err = fmt.Errorf("ctxdb: chat %s: %w", chat, ErrSessionBusy)
	}
	return out, err
}

// leaseChat takes the lease of an existing chat session. When another
// process holds it (AF-04) one system reply is queued per chat per holder and
// the row stays queued (busy = true).
func (t *Tx) leaseChat(platform, chat, sid string, isNew bool) (bool, error) {
	if isNew {
		return false, nil
	}
	_, err := t.AcquireLease(sid, false)
	if errors.Is(err, ErrSessionBusy) {
		return true, t.systemNotice(platform, chat, "busy_notice:"+chat+":"+sid, "session in use elsewhere; message queued")
	}
	return false, err
}

// chatSession returns the latest conversation session of the chat, or a new
// session (with lease and conversation row) when absent or purging.
func (t *Tx) chatSession(platform, chat string) (string, bool, error) {
	var sid, txid string
	err := t.Get(`SELECT c.session_id, coalesce((SELECT value FROM session_labels l WHERE l.session_id = c.session_id AND l.key = 'purge_state'
		ORDER BY l.id DESC LIMIT 1), '') FROM gateway_conversations c WHERE c.platform = ? AND c.chat_id = ? AND c.thread_id = ''
		ORDER BY c.created_at DESC, c.rowid DESC LIMIT 1`, []any{platform, chat}, &sid, &txid)
	switch {
	case err == nil && txid == "":
		return sid, false, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return "", false, err
	}
	sid = newID()
	if _, err := t.CreateSession(Session{ID: sid, Origin: "gateway", Cwd: ""}); err != nil {
		return "", false, err
	}
	if _, err := t.Exec("INSERT INTO gateway_conversations(platform, chat_id, session_id, created_at) VALUES (?,?,?,?)",
		platform, chat, sid, t.s.clk.WallMs()); err != nil {
		return "", false, err
	}
	if txid != "" {
		return sid, true, t.systemNotice(platform, chat, "purge_notice:"+chat+":"+txid, "previous conversation history is being deleted; continuing in a new session")
	}
	return sid, true, nil
}

// systemNotice queues one `__system__` delivery, deduplicated by key.
func (t *Tx) systemNotice(platform, chat, key, body string) error {
	n, err := t.Exec("INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('gateway', ?, '1', ?) ON CONFLICT DO NOTHING", key, t.s.clk.WallMs())
	if err != nil || n == 0 {
		return err
	}
	_, err = t.EnqueueDelivery(&Delivery{IdemKey: "sys:" + key, Platform: platform, Chat: chat, Body: body})
	return err
}

// Delivery is one outbox row.
type Delivery struct {
	IdemKey, Platform, Chat, Body, Media string
	EventID                              int64 // 0 only for `__system__` notices
}

// EnqueueDelivery inserts an outbox row; an existing idem_key with the same
// chat/body/media returns the existing id, a different one is
// idempotency_conflict (messages_send, §11.11.10).
func (t *Tx) EnqueueDelivery(d *Delivery) (int64, error) {
	var id int64
	var chat, body, media string
	err := t.Get("SELECT id, chat_id, body, coalesce(media_blob_sha, '') FROM gateway_deliveries WHERE idem_key = ?", []any{d.IdemKey}, &id, &chat, &body, &media)
	switch {
	case err == nil && chat == d.Chat && body == d.Body && media == d.Media:
		return id, nil
	case err == nil:
		return 0, fmt.Errorf("ctxdb: idem_key %s: %w", d.IdemKey, ErrIdempotencyConflict)
	case !errors.Is(err, ErrNotFound):
		return 0, err
	}
	var ev any
	sid := SystemSession
	if d.EventID != 0 {
		ev = d.EventID
		if ierr := t.Get("SELECT session_id FROM events WHERE id = ?", []any{d.EventID}, &sid); ierr != nil {
			return 0, ierr
		}
	}
	now := t.s.clk.WallMs()
	if ierr := t.Get(`INSERT INTO gateway_deliveries(idem_key, platform, chat_id, event_id, body, media_blob_sha, status, next_at, updated_at)
		VALUES (?,?,?,?,?,?,'queued',?,?) RETURNING id`, []any{d.IdemKey, d.Platform, d.Chat, ev, d.Body, nullable(d.Media), now, now}, &id); ierr != nil {
		return 0, ierr
	}
	refs, err := BlobRefsOf("gateway_delivery", strconv.FormatInt(id, 10), map[string]string{"media_blob_sha": d.Media}, nil)
	if err != nil {
		return 0, err
	}
	return id, t.InsertRefs(sid, now, refs)
}

// ClaimDelivery moves the oldest due delivery of a visible session (or
// `__system__`) from queued to sending; the guard rejects labelled sessions.
func (s *Store) ClaimDelivery(ctx context.Context) (int64, error) {
	ids, err := s.strings(ctx, `SELECT d.id FROM gateway_deliveries d WHERE d.status = 'queued' AND d.next_at <= ?
		AND (d.event_id IS NULL OR `+notPurging("(SELECT session_id FROM events e WHERE e.id = d.event_id)")+`) ORDER BY d.next_at, d.id LIMIT 1`, s.clk.WallMs())
	if err != nil || len(ids) == 0 {
		return 0, errors.Join(err, fmt.Errorf("ctxdb: no due delivery: %w", ErrNotFound))
	}
	id, err := strconv.ParseInt(ids[0], 10, 64)
	if err != nil {
		return 0, wrap(err)
	}
	return id, s.Write(ctx, func(t *Tx) error {
		n, err := t.Exec("UPDATE gateway_deliveries SET status = 'sending', updated_at = ? WHERE id = ? AND status = 'queued'", s.clk.WallMs(), id)
		if err == nil && n == 0 {
			err = fmt.Errorf("ctxdb: delivery %d purged: %w", id, ErrNotFound)
		}
		return err
	})
}

// SpoolInbound writes one line to run/inbound_spool/<boot>-<pid>-<start>-<seq>.jsonl
// and fsyncs file and directory; only then may the push be acked (§11.11.7).
// Used for every inbound the platform will not redeliver (AF-06).
func (s *Store) SpoolInbound(seq int, m *Inbound) error {
	const dir = "run/inbound_spool"
	if err := s.root.MkdirAll(dir, 0o700); err != nil {
		return wrap(err)
	}
	name := fmt.Sprintf("%s-%d-%s-%06d.jsonl", s.id.BootID, s.id.PID, s.id.Start, seq)
	f, err := s.root.OpenFile(path.Join(dir, name), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return wrap(err)
	}
	_, err = f.Write(append([]byte(jsonOf(m)), '\n'))
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("ctxdb: spool: %w", err)
	}
	return syncRootDir(s.root, dir)
}

// IngestSpool ingests spool files in name order, line order; a truncated
// last line (crash before fsync, never acked) is ignored. Files are removed
// only after COMMIT; duplicates are absorbed by the primary key.
func (s *Store) IngestSpool(ctx context.Context) (int, error) {
	const dir = "run/inbound_spool"
	ents, err := fs.ReadDir(s.root.FS(), dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	total := 0
	for _, n := range names {
		b, err := s.root.ReadFile(path.Join(dir, n))
		if err != nil {
			return total, wrap(err)
		}
		var msgs []Inbound
		lines := strings.Split(string(b), "\n")
		for _, l := range lines[:len(lines)-1] { // last segment = truncated line (never acked) or ""
			var m Inbound
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				return total, fmt.Errorf("ctxdb: spool %s: %w", n, ErrInboundInvalid)
			}
			msgs = append(msgs, m)
		}
		if err := s.IngestInbound(ctx, msgs, "", ""); err != nil {
			return total, err
		}
		total += len(msgs)
		if err := removeIfExists(s.root, path.Join(dir, n)); err != nil {
			return total, err
		}
	}
	return total, nil
}
