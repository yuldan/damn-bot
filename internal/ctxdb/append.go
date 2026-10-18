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
}

// CreateSession inserts a session and acquires its lease in the same
// transaction (fork/rewind/delegate rule, §11.11.10). Guard P on parent_id.
func (t *Tx) CreateSession(ss Session) (int64, error) {
	var parent, fork any
	if ss.ParentID != "" {
		parent, fork = ss.ParentID, ss.ForkSeq
	}
	if ss.Origin == "" {
		ss.Origin = "tui"
	}
	_, err := t.Exec(`INSERT INTO sessions(id, parent_id, fork_seq, origin, cwd, model, config_sha, context_tier, created_at)
		VALUES (?,?,?,?,?,'test/model','cfg','',?)`, ss.ID, parent, fork, ss.Origin, ss.Cwd, t.s.clk.WallMs())
	if err != nil {
		return 0, err
	}
	return t.AcquireLease(ss.ID, false)
}

// CreateSession creates a session (+ lease) in its own transaction.
func (s *Store) CreateSession(ctx context.Context, ss Session) (tok int64, err error) {
	err = s.Write(ctx, func(t *Tx) error { tok, err = t.CreateSession(ss); return err })
	return tok, err
}

// Event is one transcript row (SPEC §11.2 events).
type Event struct {
	Kind, Role, Content     string
	TurnID, ToolCallID      string
	ToolName, Payload, Blob string   // Blob = events.blob_sha
	Attach                  []string // event_attachments.blob_sha in order
}

// EventHash = sha256(prev_hash|session_id|seq|kind|content|payload|blob_sha).
func EventHash(prev, sid string, seq int64, kind, content, payload, blob string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{prev, sid, strconv.FormatInt(seq, 10), kind, content, payload, blob}, "|")))
	return hex.EncodeToString(h[:])
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// AppendEvent appends one hash-chained event plus its attachments and
// blob_refs in t. Guards P+F apply (lease token of this process).
func (t *Tx) AppendEvent(sid string, e Event) (int64, error) {
	if e.Payload == "" {
		e.Payload = "{}"
	}
	var seq int64
	var prev string
	err := t.Get("SELECT seq, hash FROM events WHERE session_id = ? ORDER BY seq DESC LIMIT 1", []any{sid}, &seq, &prev)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	seq++
	now := t.s.clk.WallMs()
	var id int64
	err = t.Get(`INSERT INTO events(session_id, seq, turn_id, kind, role, content, tool_call_id, tool_name, payload, blob_sha, prev_hash, hash, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`,
		[]any{sid, seq, nullable(e.TurnID), e.Kind, nullable(e.Role), e.Content, nullable(e.ToolCallID), nullable(e.ToolName),
			e.Payload, nullable(e.Blob), prev, EventHash(prev, sid, seq, e.Kind, e.Content, e.Payload, e.Blob), now}, &id)
	if err != nil {
		return 0, err
	}
	refs, err := BlobRefsOf("event", strconv.FormatInt(id, 10), map[string]string{"blob_sha": e.Blob}, map[string]string{"payload": e.Payload})
	if err != nil {
		return 0, err
	}
	for i, sha := range e.Attach {
		if _, err := t.Exec("INSERT INTO event_attachments(event_id, ord, blob_sha) VALUES (?,?,?)", id, i, sha); err != nil {
			return 0, err
		}
		refs = append(refs, BlobRef{Sha: sha, OwnerKind: "event_attachment", OwnerID: fmt.Sprintf("%d:%d", id, i), RefKey: "blob_sha"})
	}
	return id, t.InsertRefs(sid, now, refs)
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
		for _, e := range evs {
			id, err := t.AppendEvent(sid, e)
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
	reHex64     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reRefKey    = regexp.MustCompile(`(^|_)blob_shas?$`)
	nonRefSha64 = map[string]bool{"preview_sha": true, "args_sha": true, "payload_sha": true}
)

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
		if doc == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			return nil, fmt.Errorf("ctxdb: %s.%s not JSON: %w", kind, col, ErrInternal)
		}
		if err := walkRefs(v, col, "", kind, id, &out); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RefKey+out[i].Sha < out[j].RefKey+out[j].Sha })
	return out, nil
}

func walkRefs(v any, path, key, kind, id string, out *[]BlobRef) error {
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if err := walkRefs(c, path+"."+k, k, kind, id, out); err != nil {
				return err
			}
		}
	case []any:
		for i, c := range x {
			if err := walkRefs(c, fmt.Sprintf("%s[%d]", path, i), key, kind, id, out); err != nil {
				return err
			}
		}
	case string:
		switch {
		case reRefKey.MatchString(key) && x != "":
			*out = append(*out, BlobRef{Sha: x, OwnerKind: kind, OwnerID: id, RefKey: path})
		case reHex64.MatchString(x) && !nonRefSha64[key]:
			return fmt.Errorf("ctxdb: sha under non-ref key %q: %w", path, ErrInternal)
		}
	}
	return nil
}

// InsertRefs inserts blob_refs rows after checking, under the write lock,
// that each sha has a blobs row whose latest status is not expired (or was
// published in this transaction). Re-referencing an expired sha → blob_expired.
func (t *Tx) InsertRefs(sid string, created int64, refs []BlobRef) error {
	for _, r := range refs {
		if err := t.checkLiveBlob(r.Sha); err != nil {
			return err
		}
		_, err := t.Exec(`INSERT INTO blob_refs(sha256, owner_kind, owner_id, ref_key, session_id, created_at)
			VALUES (?,?,?,?,?,?) ON CONFLICT DO NOTHING`, r.Sha, r.OwnerKind, r.OwnerID, r.RefKey, sid, created)
		if err != nil {
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
	switch {
	case err != nil:
		return err
	case exists == 0 || last == "expired":
		return fmt.Errorf("ctxdb: blob %s: %w", sha, ErrBlobExpired)
	}
	return nil
}
