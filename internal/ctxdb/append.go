package ctxdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Session is a new sessions row.
type Session struct {
	ID, ParentID string
	ForkSeq      int64
	Origin, Cwd  string
	Model        string // provider/model; empty = the caller did not resolve one
	ConfigSHA    string // sessions.config_sha snapshot
	ContextTier  string // frozen context tier (P15); empty at creation
	// GatewayIdentity is the gateway principal a child inherits; local
	// sessions use "". It is validation input, not a sessions column.
	GatewayIdentity string
}

// CreateSession inserts a session and acquires its lease in the same
// transaction (fork/rewind/delegate rule, §11.11.10). Guard P on parent_id.
//
//nolint:gocritic // consolidated for CreateSession
func (t *Tx) CreateSession(ss Session) (int64, error) {
	var parent, fork any
	if ss.ParentID != "" {
		parent, fork = ss.ParentID, ss.ForkSeq
	}
	if ss.Origin == "" {
		ss.Origin = "tui"
	}
	identity, err := t.checkParent(&ss)
	if err != nil {
		return 0, err
	}
	_, err = t.Exec(`INSERT INTO sessions(id, parent_id, fork_seq, origin, cwd, model, config_sha, context_tier, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, ss.ID, parent, fork, ss.Origin, ss.Cwd, ss.Model, ss.ConfigSHA, ss.ContextTier, t.s.clk.WallMs())
	if err != nil {
		return 0, err
	}
	if identity != "" {
		if _, err := t.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
			VALUES (?, 'gateway_chat', ?, ?)`, ss.ID, identity, t.s.clk.WallMs()); err != nil {
			return 0, err
		}
	}
	return t.AcquireLease(ss.ID, false)
}

// checkParent rejects a child whose parent belongs to another project (cwd)
// or gateway identity, and returns the identity the child inherits. Origin
// must match too, except for delegate: SPEC §11.2 allows a delegate link
// without a fork, but its principal and cwd still come from the parent.
func (t *Tx) checkParent(ss *Session) (string, error) {
	if ss.ParentID == "" {
		return "", nil
	}
	var origin, cwd, identity string
	err := t.Get(`SELECT s.origin, s.cwd, coalesce((SELECT l.value FROM session_labels l
		WHERE l.session_id = s.id AND l.key = 'gateway_chat' ORDER BY l.id DESC LIMIT 1), '')
		FROM sessions s WHERE s.id = ?1`, []any{ss.ParentID}, &origin, &cwd, &identity)
	if err != nil {
		return "", fmt.Errorf("ctxdb: parent %s: %w", ss.ParentID, err)
	}
	if cwd != ss.Cwd || identity != ss.GatewayIdentity || (ss.Origin != "delegate" && origin != ss.Origin) {
		return "", fmt.Errorf("ctxdb: parent %s origin/cwd/identity mismatch: %w", ss.ParentID, ErrInvalidSeq)
	}
	return identity, nil
}

// CreateSession creates a session (+ lease) in its own transaction.
func (s *Store) CreateSession(ctx context.Context, ss Session) (tok int64, err error) { //nolint:gocritic // value API: callers pass a literal
	err = s.Write(ctx, func(t *Tx) (cerr error) { tok, cerr = t.CreateSession(ss); return cerr })
	return tok, err
}

// Event is one transcript row (SPEC §11.2 events). Append ignores the
// read-only fields (ID, Seq, SessionID, CreatedAt); History fills them and
// Seq is the row's per-session events.seq, the same value fork_seq uses.
type Event struct {
	ID, Seq                 int64
	SessionID               string
	Kind, Role, Content     string
	TurnID, ToolCallID      string
	ToolName, Payload, Blob string   // Blob = events.blob_sha
	Attach                  []string // event_attachments.blob_sha in order
	CreatedAt               int64
}

// EventHash = sha256(prev_hash|session_id|seq|kind|content|payload|blob_sha).
func EventHash(prev, sid string, seq int64, kind, content, payload, blob string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{prev, sid, strconv.FormatInt(seq, 10), kind, content, payload, blob}, "|")))
	return hex.EncodeToString(h[:])
}

func (s *Store) eventHash(prev, sid string, seq int64, kind, content, payload, blob string) string {
	if s.codec == nil {
		return EventHash(prev, sid, seq, kind, content, payload, blob)
	}
	return s.codec.Hash(strings.Join([]string{prev, sid, strconv.FormatInt(seq, 10), kind, content, payload, blob}, "|"))
}

func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// AppendEvent appends one hash-chained event plus its attachments and
// blob_refs in t. Guards P+F apply (lease token of this process).
func (t *Tx) AppendEvent(sid string, e *Event) (int64, error) {
	payload := cmp(e.Payload, "{}")
	var seq int64
	var prev string
	err := t.Get("SELECT seq, hash FROM events WHERE session_id = ? ORDER BY seq DESC LIMIT 1", []any{sid}, &seq, &prev)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	seq++
	now := t.s.clk.WallMs()
	identity := sid + "|" + strconv.FormatInt(seq, 10)
	contentEnc, contentStored, err := t.s.sealRow("events.content", identity, e.Content, "")
	if err != nil {
		return 0, err
	}
	payloadEnc, payloadStored, err := t.s.sealRow("events.payload", identity, payload, "{}")
	if err != nil {
		return 0, err
	}
	var id int64
	err = t.Get(`INSERT INTO events(session_id, seq, turn_id, kind, role, content, content_enc, tool_call_id, tool_name, payload, payload_enc, blob_sha, prev_hash, hash, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		[]any{
			sid, seq, nullable(e.TurnID), e.Kind, nullable(e.Role), contentStored, nullableBytes(contentEnc), nullable(e.ToolCallID), nullable(e.ToolName),
			payloadStored, nullableBytes(payloadEnc), nullable(e.Blob), prev, t.s.eventHash(prev, sid, seq, e.Kind, e.Content, payload, e.Blob), now,
		}, &id)
	if err != nil {
		return 0, err
	}
	refs, err := BlobRefsOf("event", strconv.FormatInt(id, 10), map[string]string{"blob_sha": e.Blob}, map[string]string{"payload": payload})
	for i := 0; err == nil && i < len(e.Attach); i++ {
		_, err = t.Exec("INSERT INTO event_attachments(event_id, ord, blob_sha) VALUES (?,?,?)", id, i, e.Attach[i])
		refs = append(refs, BlobRef{Sha: e.Attach[i], OwnerKind: "event_attachment", OwnerID: fmt.Sprintf("%d:%d", id, i), RefKey: "blob_sha"})
	}
	if err != nil {
		return 0, err
	}
	if e.Blob != "" {
		if err := t.indexBlobFTS(e.Blob, id); err != nil {
			return 0, err
		}
	}
	return id, t.InsertRefs(sid, now, refs)
}

func cmp(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Append publishes pending blobs and appends events in one transaction
// (Store.Append, §4.6). Returns the event ids.
func (s *Store) Append(ctx context.Context, sid string, pend []*PendingBlob, evs ...Event) ([]int64, error) {
	var ids []int64
	err := s.Write(ctx, func(t *Tx) error {
		ids = ids[:0]
		for _, p := range pend {
			if err := t.Publish(p); err != nil {
				return err
			}
		}
		for i := range evs {
			id, err := t.AppendEvent(sid, &evs[i])
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	return ids, err
}

// BlobRef is one blob_refs row (ADR-039).
type BlobRef struct{ Sha, OwnerKind, OwnerID, RefKey string }

var (
	reHex64      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reRefKey     = regexp.MustCompile(`(^|_)blob_shas?$`)
	reCarrierSha = regexp.MustCompile(`^payload\.carriers\[\d+\]\.sha$`)
)

// nonRefSha64 is the closed list of 64-hex keys that are not blob refs
// (ADR-093, TS-P04: approval_request.preview_sha/args_sha, carrier payload_sha).
// The carrier ref's short "sha" is exempt only at carriers[N].sha (SPEC §4.1).
func nonRefSha64(key, path string) bool {
	switch key {
	case "preview_sha", "args_sha", "payload_sha":
		return true
	case "sha":
		return reCarrierSha.MatchString(path)
	}
	return false
}

// BlobRefsOf derives every blob reference of one owner row (§11.6): cols are
// ref columns (name → sha), docs are JSON documents (column → text) scanned
// for keys blob_sha / *_blob_sha / *_blob_shas. A 64-hex value under any
// other key is rejected (payload validator), except the closed non-ref list.
func BlobRefsOf(kind, id string, cols, docs map[string]string) ([]BlobRef, error) {
	var out []BlobRef
	for k, v := range cols {
		if v != "" {
			out = append(out, BlobRef{Sha: v, OwnerKind: kind, OwnerID: id, RefKey: k})
		}
	}
	for col, doc := range docs {
		var v any
		if doc == "" {
			continue
		}
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			return nil, fmt.Errorf("ctxdb: %s.%s not JSON: %w", kind, col, ErrInternal)
		}
		if err := walkRefs(v, col, "", BlobRef{OwnerKind: kind, OwnerID: id}, &out); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RefKey+out[i].Sha < out[j].RefKey+out[j].Sha })
	return out, nil
}

func walkRefs(v any, path, key string, owner BlobRef, out *[]BlobRef) error {
	var err error
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			err = errors.Join(err, walkRefs(c, path+"."+k, k, owner, out))
		}
	case []any:
		for i, c := range x {
			err = errors.Join(err, walkRefs(c, fmt.Sprintf("%s[%d]", path, i), key, owner, out))
		}
	case string:
		switch {
		case reRefKey.MatchString(key) && x != "":
			owner.Sha, owner.RefKey = x, path
			*out = append(*out, owner)
		case reHex64.MatchString(x) && !nonRefSha64(key, path):
			err = fmt.Errorf("ctxdb: sha under non-ref key %q: %w", path, ErrInternal)
		}
	}
	return err
}

// InsertRefs inserts blob_refs rows after checking, under the write lock,
// that each sha has a blobs row whose latest status is not expired (or was
// published in this transaction). Re-referencing an expired sha → blob_expired.
func (t *Tx) InsertRefs(sid string, created int64, refs []BlobRef) error {
	for _, r := range refs {
		if err := t.checkLiveBlob(r.Sha); err != nil {
			return err
		}
		if _, err := t.Exec(`INSERT INTO blob_refs(sha256, owner_kind, owner_id, ref_key, session_id, created_at)
			VALUES (?,?,?,?,?,?) ON CONFLICT DO NOTHING`, r.Sha, r.OwnerKind, r.OwnerID, r.RefKey, sid, created); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) checkLiveBlob(sha string) error {
	var exists int
	var last string
	err := t.Get(`SELECT EXISTS (SELECT 1 FROM blobs WHERE sha256 = ?1),
		coalesce((SELECT kind FROM blob_expirations WHERE sha256 = ?1 ORDER BY id DESC LIMIT 1), '')`, []any{sha}, &exists, &last)
	if err == nil && (exists == 0 || last == "expired") {
		err = fmt.Errorf("ctxdb: blob %s: %w", sha, ErrBlobExpired)
	}
	return err
}
