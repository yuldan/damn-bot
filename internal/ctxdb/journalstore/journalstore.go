// Package journalstore is the actions/action_undos storage of
// internal/journal (SPEC §11.8, ADR-074, ADR-117). It is the only writer of
// the action journal: Record publishes the before/after images as
// content-addressed blobs and inserts the actions row (plus its
// tool_dispatch event) in one transaction before the side effect runs.
package journalstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/journal"
)

// Defaults of SPEC §18.1 ([journal] retention_days / max_bytes).
const (
	DefaultRetentionDays = 30
	DefaultMaxBytes      = 2 << 30
	dayMs                = 24 * 3_600_000
)

// Options configures retention and the clock used for created_at/cutoff.
// Zero values select the SPEC defaults.
type Options struct {
	RetentionDays int
	MaxBytes      int64
	Now           func() int64 // unix ms; nil = time.Now
}

func (o *Options) withDefaults() {
	if o.RetentionDays == 0 {
		o.RetentionDays = DefaultRetentionDays
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.Now == nil {
		o.Now = func() int64 { return time.Now().UnixMilli() }
	}
}

// Store implements journal.Store over one ctxdb.Store.
type Store struct {
	db  *ctxdb.Store
	opt Options
}

// New returns the journal storage of db.
func New(db *ctxdb.Store, o *Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("journalstore: nil store")
	}
	opt := Options{}
	if o != nil {
		opt = *o
	}
	opt.withDefaults()
	return &Store{db: db, opt: opt}, nil
}

// inverse is the actions.inverse document (§18.1). Blob shas must live under
// *_blob_sha keys so ctxdb.BlobRefsOf derives the blob_refs rows.
type inverse struct {
	Path   string    `json:"path"`
	MoveTo string    `json:"move_to,omitempty"`
	Before *invImage `json:"before,omitempty"`
	After  *invImage `json:"after,omitempty"`
	CallID string    `json:"call_id,omitempty"`
	Tool   string    `json:"tool,omitempty"`
}

type invImage struct {
	BlobSHA string `json:"blob_sha"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
}

// Record publishes the images and inserts one actions row in the same
// transaction as its tool_dispatch event. The caller must perform the side
// effect only after Record returns nil (SPEC §4.3 step 8).
func (s *Store) Record(ctx context.Context, r journal.Record) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, err
	}
	var pend []*ctxdb.PendingBlob
	inv := inverse{Path: r.Path, MoveTo: r.MoveTo, CallID: r.CallID, Tool: r.ToolName}
	held := map[string]int64{}
	img := func(sn *journal.Snapshot, field **invImage) error {
		if sn == nil {
			return nil
		}
		p, err := s.db.PutBlob(sn.Data, "application/octet-stream")
		if err != nil {
			return err
		}
		pend = append(pend, p)
		held[p.Sha] = p.Size
		*field = &invImage{BlobSHA: p.Sha, Size: p.Size, Mode: uint32(sn.Mode & keepMode)}
		return nil
	}
	if err := errors.Join(img(r.Before, &inv.Before), img(r.After, &inv.After)); err != nil {
		return 0, fmt.Errorf("journalstore: images: %w", err)
	}
	doc, err := json.Marshal(&inv)
	if err != nil {
		return 0, fmt.Errorf("journalstore: inverse: %w", err)
	}
	var bytesHeld int64
	for _, n := range held {
		bytesHeld += n
	}
	var id int64
	err = s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		for _, p := range pend {
			if err := tx.Publish(p); err != nil {
				return err
			}
		}
		ev, err := tx.AppendEvent(r.SessionID, &ctxdb.Event{
			Kind: "tool_dispatch", TurnID: r.TurnID, ToolCallID: r.CallID, ToolName: r.ToolName,
		})
		if err != nil {
			return err
		}
		now := s.opt.Now()
		if err := tx.Get(`INSERT INTO actions(session_id, turn_id, event_id, kind, reversibility, subject, inverse, bytes_held, created_at)
			VALUES (?1,?2,?3,?4,'reversible',?5,?6,?7,?8) RETURNING id`,
			[]any{r.SessionID, null(r.TurnID), ev, string(r.Kind), r.Path, string(doc), bytesHeld, now}, &id); err != nil {
			return err
		}
		refs, err := ctxdb.BlobRefsOf("action", strconv.FormatInt(id, 10), nil, map[string]string{"inverse": string(doc)})
		if err != nil {
			return err
		}
		return tx.InsertRefs(r.SessionID, now, refs)
	})
	if err != nil {
		return 0, fmt.Errorf("journalstore: record: %w", err)
	}
	return id, nil
}

// keepMode is the mode an undo restores: permission bits plus
// setuid/setgid/sticky (matches codetools keepMode, CODE-TOOLS §5.4).
const keepMode = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// actionColumns is shared by Get and List; open matches SPEC §11.8's open
// state list, so undo refuses while a turn can still touch the file.
const actionColumns = `id, session_id, coalesce(turn_id, ''), event_id, kind, reversibility, subject, inverse, bytes_held, created_at,
	EXISTS (SELECT 1 FROM turn_journal tj WHERE tj.turn_id = actions.turn_id
		AND tj.state IN ('running','tool_pending','tool_done','awaiting_confirmation','awaiting_approval'))`

// Get returns one action. A missing session is reported as not_found (guard
// P visibility), not as an internal error.
func (s *Store) Get(ctx context.Context, id int64) (journal.Action, error) {
	var a journal.Action
	var inv, kind, rev string
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		if err := tx.Get("SELECT "+actionColumns+" FROM actions WHERE id = ?1", []any{id},
			&a.ID, &a.SessionID, &a.TurnID, &a.EventID, &kind, &rev, &a.Path, &inv,
			&a.BytesHeld, &a.CreatedAt, &a.Open); err != nil {
			return err
		}
		a.Kind, a.Reversibility = journal.Kind(kind), journal.Reversibility(rev)
		cut, err := cutoff(tx, s.opt)
		if err != nil {
			return err
		}
		a.Expired = a.CreatedAt < cut
		return decode(&a, inv)
	})
	if err != nil {
		return journal.Action{}, fmt.Errorf("journalstore: get action %d: %w", id, err)
	}
	return a, s.db.Visible(ctx, a.SessionID)
}

// List returns the newest limit actions of sessionID (0 = all). Row ids are
// read in one transaction, then each action through Get (a handful of rows
// per undo command; no unbounded scan).
func (s *Store) List(ctx context.Context, sessionID string, limit int) ([]journal.Action, error) {
	if err := s.db.Visible(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("journalstore: list: %w", err)
	}
	if limit < 0 {
		limit = 0
	}
	var ids []int64
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		q := "SELECT id FROM actions WHERE session_id = ?1 ORDER BY id DESC"
		args := []any{sessionID}
		if limit > 0 {
			q += " LIMIT ?2"
			args = append(args, limit)
		}
		var qerr error
		ids, qerr = tx.Ints(q, args...)
		return qerr
	})
	if err != nil {
		return nil, fmt.Errorf("journalstore: list: %w", err)
	}
	out := make([]journal.Action, 0, len(ids))
	for _, id := range ids {
		a, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// Blob returns a before/after image; a blob no longer on disk is ErrBlob
// (expired blobs keep ctxdb.ErrBlobExpired in the chain).
func (s *Store) Blob(ctx context.Context, sha string) ([]byte, error) {
	b, err := s.db.ReadBlob(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", journal.ErrBlob, err)
	}
	return b, nil
}

// Finish appends the undo event and its action_undos row in one transaction
// in the caller session (guard F), as SPEC §18.1 requires.
func (s *Store) Finish(ctx context.Context, u journal.Undo) error {
	switch u.Status {
	case journal.Undone, journal.Conflict, journal.Expired, journal.Failed:
	default:
		return fmt.Errorf("journalstore: undo status %q: %w", u.Status, journal.ErrIrreversible)
	}
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		ev, err := tx.AppendEvent(u.SessionID, &ctxdb.Event{
			Kind: "undo", Payload: fmt.Sprintf(`{"action_id":%d,"status":%q}`, u.ActionID, u.Status),
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO action_undos(action_id, status, detail, event_id, created_at) VALUES (?1,?2,?3,?4,?5)`,
			u.ActionID, string(u.Status), u.Detail, ev, s.opt.Now())
		return err
	})
	if err != nil {
		return fmt.Errorf("journalstore: finish undo: %w", err)
	}
	return nil
}

// cutoff is T_cut (§18.1): max(now − retention, created_at of the oldest
// action that still fits when bytes_held is summed newest-first ≤ max_bytes).
func cutoff(tx *ctxdb.Tx, o Options) (int64, error) {
	now := o.Now()
	var oldest int64
	err := tx.Get(`SELECT COALESCE(MIN(created_at), 0) FROM (
		SELECT created_at, SUM(bytes_held) OVER (ORDER BY id DESC ROWS UNBOUNDED PRECEDING) AS held
		FROM actions) WHERE held <= ?1`, []any{o.MaxBytes}, &oldest)
	if err != nil {
		return 0, err
	}
	cut := now - int64(o.RetentionDays)*dayMs
	if oldest == 0 {
		return now, nil // not even the newest action fits the byte cap
	}
	return max(cut, oldest), nil
}

func decode(a *journal.Action, doc string) error {
	var x inverse
	if err := json.Unmarshal([]byte(doc), &x); err != nil {
		return fmt.Errorf("journalstore: inverse of action %d: %w", a.ID, err)
	}
	a.Path, a.MoveTo = x.Path, x.MoveTo
	a.Before = image(x.Before)
	a.After = image(x.After)
	return nil
}

func image(i *invImage) *journal.Image {
	if i == nil {
		return nil
	}
	return &journal.Image{BlobSHA: i.BlobSHA, Size: i.Size, Mode: fs.FileMode(i.Mode) & keepMode}
}
