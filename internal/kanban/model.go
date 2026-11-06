package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Errors of the board surface. Callers map them to exit codes or tool codes;
// no error carries a path from outside the workspace.
var (
	ErrNotFound         = errors.New("kanban: not found")
	ErrBoardNotFound    = errors.New("kanban: board not found")
	ErrInvalid          = errors.New("kanban: invalid argument")
	ErrTransition       = errors.New("kanban: illegal transition")
	ErrClaimed          = errors.New("kanban: task already claimed")
	ErrNotOwner         = errors.New("kanban: worker does not own the lease")
	ErrLeaseHeld        = errors.New("kanban: lease still valid")
	ErrCycle            = errors.New("kanban: dependency cycle")
	ErrOutsideWorkspace = errors.New("kanban: path outside workspace")
	ErrBlockedURL       = errors.New("kanban: URL blocked")
	ErrTooLarge         = errors.New("kanban: attachment too large")
	ErrNotConfigured    = errors.New("kanban: LLM auxiliary not configured")
	ErrOrchestratorOnly = errors.New("kanban: orchestrator only")
)

// Status is one kanban_tasks.status value (the schema's closed set).
type Status string

// Board states. "archive" is a flag on a done task, not a seventh status.
const (
	StatusBacklog    Status = "backlog"
	StatusReady      Status = "ready"
	StatusInProgress Status = "in_progress"
	StatusReview     Status = "review"
	StatusDone       Status = "done"
	StatusBlocked    Status = "blocked"
)

func (s Status) valid() bool {
	switch s {
	case StatusBacklog, StatusReady, StatusInProgress, StatusReview, StatusDone, StatusBlocked:
		return true
	}
	return false
}

// live is a task that can still be edited or moved.
func (s Status) live() bool { return s != StatusDone }

// Board is one kanban_boards row.
type Board struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Task is one board card plus the metadata folded from its event log.
type Task struct {
	ID             string       `json:"id"`
	BoardID        string       `json:"board_id"`
	Title          string       `json:"title"`
	Body           string       `json:"body"`
	Status         Status       `json:"status"`
	Assignee       string       `json:"assignee"`
	Priority       int          `json:"priority"`
	Labels         []string     `json:"labels"`
	DueAt          time.Time    `json:"due_at,omitzero"`
	SessionID      string       `json:"session_id,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	Model          string       `json:"model,omitempty"`
	Spec           string       `json:"spec,omitempty"`
	Archived       bool         `json:"archived"`
	Links          []string     `json:"links,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	Comments       int          `json:"comments"`
	Lease          *Lease       `json:"lease,omitempty"`
	Runs           []Run        `json:"runs,omitempty"`
	NeedsAttention bool         `json:"needs_attention,omitempty"`
}

// Attachment is one file or HTTPS URL attached to a card. A file stays in the
// workspace (only its relative path and hash are stored); a URL stores the
// fetched bytes' hash and size, never credentials.
type Attachment struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // file|url
	Path    string    `json:"path,omitempty"`
	URL     string    `json:"url,omitempty"`
	Size    int64     `json:"size"`
	SHA256  string    `json:"sha256"`
	AddedAt time.Time `json:"added_at"`
}

// TaskSpec is the create argument. Priority nil keeps the schema default (2).
type TaskSpec struct {
	Title    string
	Body     string
	Status   Status
	Assignee string
	Priority *int
	Labels   []string
	DueAt    *time.Time
	Links    []string
}

// ChildSpec is one decomposed card.
type ChildSpec struct {
	Title    string   `json:"title"`
	Body     string   `json:"body,omitempty"`
	Assignee string   `json:"assignee,omitempty"`
	Priority *int     `json:"priority,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Links    []string `json:"links,omitempty"`
}

// Filter narrows Tasks.
type Filter struct {
	Status   Status
	Assignee string
	Limit    int
	Archived bool
}

// Event is one append-only kanban_task_events row.
type Event struct {
	ID        int64           `json:"id"`
	TaskID    string          `json:"task_id"`
	Actor     string          `json:"actor"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Payload is the union of the event payloads this package writes. New fields
// stay omitempty so old rows decode unchanged.
type Payload struct {
	From     Status      `json:"from,omitempty"`
	To       Status      `json:"to,omitempty"`
	Reason   string      `json:"reason,omitempty"`
	Assignee string      `json:"assignee,omitempty"`
	Link     string      `json:"link,omitempty"`
	Unlink   string      `json:"unlink,omitempty"`
	Attach   *Attachment `json:"attach,omitempty"`
	Detach   string      `json:"detach,omitempty"`
	Text     string      `json:"text,omitempty"`
	Lease    *Lease      `json:"lease,omitempty"`
	Run      *Run        `json:"run,omitempty"`
	Model    string      `json:"model,omitempty"`
	Spec     string      `json:"spec,omitempty"`
	Archived bool        `json:"archived,omitempty"`
	Handoff  *Handoff    `json:"handoff,omitempty"`
}

// Lease is the worker claim. Expiry is derived from At+TTL; the event log
// keeps every renewal so a stale worker is visible.
type Lease struct {
	Worker    string    `json:"worker"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Run is one dispatcher/worker run and its billed cost. ID is the
// idempotency key: recording the same run twice bills once (REL-14).
type Run struct {
	ID         string    `json:"id"`
	Worker     string    `json:"worker"`
	Outcome    string    `json:"outcome,omitempty"`
	CostMicros int64     `json:"cost_micros"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Verify     []string  `json:"verify,omitempty"`
}

// Handoff is the structured completion contract.
type Handoff struct {
	Summary string   `json:"summary"`
	Files   []string `json:"files,omitempty"`
	Commit  string   `json:"commit,omitempty"`
	Verify  []string `json:"verify,omitempty"`
}

// Store is the board over one ctxdb store. Workspace is the attachment root.
type Store struct {
	DB        *ctxdb.Store
	Workspace string
	Now       func() time.Time
	TTL       time.Duration
	MaxAttach int64
	Fetch     Fetcher
	Resolve   func(host string) ([]net.IP, error)
	NotifyDir string
}

// Fetcher is the URL attachment download seam. It must return at most max
// bytes; the store enforces the limit again.
type Fetcher interface {
	Get(ctx context.Context, rawURL string, max int64) ([]byte, error)
}

// New returns a store. A nil clock is time.Now and a nil workspace is the
// process working directory; both are fixed by the caller for tests.
func New(db *ctxdb.Store, workspace string) *Store {
	if workspace == "" {
		if wd, err := os.Getwd(); err == nil {
			workspace = wd
		}
	}
	return &Store{
		DB: db, Workspace: workspace, Now: time.Now, TTL: 5 * time.Minute,
		MaxAttach: 8 << 20, Resolve: resolver, NotifyDir: workspace,
	}
}

func (s *Store) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func resolver(host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve %s: %w", ErrBlockedURL, host, err)
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// InitBoard creates a board once; the second call returns the same row.
func (s *Store) InitBoard(ctx context.Context, name, actor string) (Board, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return Board{}, fmt.Errorf("%w: board name must be 1..64 characters", ErrInvalid)
	}
	var b Board
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		now := s.now().UnixMilli()
		id := "b-" + slug(name)
		_, err := tx.Exec(`INSERT INTO kanban_boards(id, name, created_at) VALUES (?1, ?2, ?3)
			ON CONFLICT(name) DO NOTHING`, id, name, now)
		if err != nil {
			return fmt.Errorf("create board: %w", err)
		}
		var created int64
		if err := tx.Get(`SELECT id, name, created_at FROM kanban_boards WHERE name = ?1`, []any{name}, &b.ID, &b.Name, &created); err != nil {
			return fmt.Errorf("read board: %w", err)
		}
		b.CreatedAt = time.UnixMilli(created)
		return nil
	})
	if err != nil {
		return Board{}, fmt.Errorf("init board: %w", err)
	}
	return b, nil
}

// Boards lists every board in name order.
func (s *Store) Boards(ctx context.Context) ([]Board, error) {
	var out []Board
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, err := tx.Strings(`SELECT id FROM kanban_boards ORDER BY name`)
		if err != nil {
			return fmt.Errorf("list boards: %w", err)
		}
		for _, id := range ids {
			var b Board
			var created int64
			if err := tx.Get(`SELECT id, name, created_at FROM kanban_boards WHERE id = ?1`, []any{id}, &b.ID, &b.Name, &created); err != nil {
				return fmt.Errorf("read board %s: %w", id, err)
			}
			b.CreatedAt = time.UnixMilli(created)
			out = append(out, b)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list boards: %w", err)
	}
	return out, nil
}

func boardID(tx *ctxdb.Tx, ref string) (string, error) {
	if ref == "" {
		ref = "default"
	}
	var id string
	err := tx.Get(`SELECT id FROM kanban_boards WHERE id = ?1 OR name = ?1`, []any{ref}, &id)
	if errors.Is(err, ctxdb.ErrNotFound) {
		return "", fmt.Errorf("%w: %s", ErrBoardNotFound, ref)
	}
	if err != nil {
		return "", fmt.Errorf("read board: %w", err)
	}
	return id, nil
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slug(text string) string {
	out := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(text), "-"), "-")
	if out == "" {
		out = "task"
	}
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}
	if out == "" {
		out = "task"
	}
	return out
}
