// Package journal is the action journal of Damn-Bot: every mutating file
// action is recorded with its inverse before the side effect runs, so an
// undo can restore byte-identical files (SPEC §18.1, P26).
//
// The package declares the consumer-side interfaces (E-09); storage lives in
// internal/ctxdb/journalstore (ADR-117) and the composition root wires both.
package journal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// Kind is the actions.kind vocabulary of SPEC §11.8.
type Kind string

// Reversibility is the actions.reversibility vocabulary of SPEC §11.8.
type Reversibility string

const (
	// FileWrite records a whole-file write (write_file), including creation.
	FileWrite Kind = "file_write"
	// FileEdit records an in-place edit (edit_file).
	FileEdit Kind = "file_edit"
	// FileDelete records a delete.
	FileDelete Kind = "file_delete"
	// FileMove records a rename or move.
	FileMove Kind = "file_move"

	// Reversible means the recorded inverse restores the action exactly.
	Reversible Reversibility = "reversible"
)

// Errors classify undo refusals. Callers match with errors.Is only.
var (
	ErrConflict     = errors.New("journal: action conflicts with the current file")
	ErrExpired      = errors.New("journal: action expired")
	ErrIrreversible = errors.New("journal: action has no inverse")
	ErrPending      = errors.New("journal: action's turn is still open")
	ErrUnsafePath   = errors.New("journal: path is outside the sandbox")
	ErrBlob         = errors.New("journal: inverse blob is unavailable")
	ErrState        = errors.New("journal: file state cannot be restored")
)

// Snapshot is the content and permission bits of one side of a file action;
// nil means the path did not exist on that side. ModeSet distinguishes an
// authoritative mode 0000 from "not recorded"; Device/Inode are the journaled
// identity (0/0 on platforms without inode numbers) used to refuse an undo
// when the path was swapped for another object (JN2-1).
type Snapshot struct {
	Data          []byte
	Mode          fs.FileMode
	ModeSet       bool
	Device, Inode uint64
}

// Image is the stored form of a Snapshot: a content-addressed blob (§11.6)
// and the mode to restore. BlobSHA is a full sha256, so it is also the hash
// used to detect that a file changed after the action.
type Image struct {
	BlobSHA       string
	Size          int64
	Mode          fs.FileMode
	ModeSet       bool
	Device, Inode uint64
}

// Record is one action the caller is about to perform (SPEC §4.3 step 8:
// the row is committed before the side effect). Path and MoveTo are absolute
// resolved paths; the caller has already passed the sandbox.
type Record struct {
	SessionID string
	TurnID    string
	CallID    string
	ToolName  string
	Kind      Kind
	Path      string
	MoveTo    string // FileMove only
	Before    *Snapshot
	After     *Snapshot
}

// Validate checks the closed vocabulary and the fields undo needs.
func (r *Record) Validate() error {
	switch r.Kind {
	case FileWrite, FileEdit, FileDelete, FileMove:
	default:
		return fmt.Errorf("journal: kind %q: %w", r.Kind, ErrIrreversible)
	}
	if r.SessionID == "" || r.Path == "" || !strings.HasPrefix(r.Path, "/") {
		return fmt.Errorf("journal: record needs session and absolute path: %w", ErrIrreversible)
	}
	if r.Kind == FileMove && (r.MoveTo == "" || !strings.HasPrefix(r.MoveTo, "/")) {
		return fmt.Errorf("journal: move needs absolute destination: %w", ErrIrreversible)
	}
	if r.Before == nil && r.After == nil {
		return fmt.Errorf("journal: action has no images: %w", ErrIrreversible)
	}
	return nil
}

// Action is one stored actions row.
type Action struct {
	ID            int64
	SessionID     string
	TurnID        string
	EventID       int64
	Kind          Kind
	Reversibility Reversibility
	Path          string
	MoveTo        string
	Before        *Image
	After         *Image
	BytesHeld     int64
	CreatedAt     int64
	Expired       bool // older than T_cut (§18.1)
	Open          bool // turn_journal row of TurnID is still open
}

// Status is the action_undos.status vocabulary of SPEC §11.8.
type Status string

// Undo outcome statuses recorded in `action_undos`.
const (
	Undone   Status = "undone"
	Conflict Status = "conflict"
	Expired  Status = "expired"
	Failed   Status = "failed"
)

// Undo is the result the caller records after touching files.
type Undo struct {
	ActionID  int64
	SessionID string // caller session that records the undo (guard F)
	Status    Status
	Detail    string
}

// Store is the consumer-side interface of internal/ctxdb/journalstore:
// record before the side effect, read actions and their before-image blobs,
// and record the undo outcome.
type Store interface {
	Record(ctx context.Context, r *Record) (int64, error)
	Get(ctx context.Context, id int64) (Action, error)
	List(ctx context.Context, sessionID string, limit int) ([]Action, error)
	Blob(ctx context.Context, sha string) ([]byte, error)
	Finish(ctx context.Context, u Undo) error
}

// BatchFinisher is the optional batch form of Store.Finish: journalstore
// implements it so `undo N` writes one transaction instead of N commits.
type BatchFinisher interface {
	FinishBatch(ctx context.Context, undos []Undo) error
}

// Result is one undone action. Status is what was recorded in action_undos.
type Result struct {
	ActionID int64
	Status   Status
	Detail   string
	Files    []string // paths already restored when a later step failed
}
