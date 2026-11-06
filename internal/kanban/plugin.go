package kanban

import (
	"context"
	"fmt"
	"strings"
)

// Dashboard is the read-only data the F8 UI and PLUG-021 expose: boards,
// task rows, runs and stats. It never contains attachment bytes, bodies of
// archived cards, lease secrets, or provider keys.
type Dashboard struct {
	Boards []Board         `json:"boards"`
	Tasks  []DashboardTask `json:"tasks"`
	Runs   []Run           `json:"runs"`
	Stats  Stats           `json:"stats"`
}

// DashboardTask is one redacted card row.
type DashboardTask struct {
	ID          string `json:"id"`
	BoardID     string `json:"board_id"`
	Title       string `json:"title"`
	Status      Status `json:"status"`
	Assignee    string `json:"assignee,omitempty"`
	Priority    int    `json:"priority"`
	Archived    bool   `json:"archived"`
	UpdatedAt   string `json:"updated_at"`
	Attachments int    `json:"attachments"`
	Comments    int    `json:"comments"`
}

// DashboardData reads the plugin view. It is read-only by construction: no
// mutating method is called and bodies/specs are omitted.
func (s *Store) DashboardData(ctx context.Context, board string) (Dashboard, error) {
	boards, err := s.Boards(ctx)
	if err != nil {
		return Dashboard{}, err
	}
	tasks, err := s.Tasks(ctx, board, Filter{Limit: 0, Archived: false})
	if err != nil {
		return Dashboard{}, err
	}
	st, err := s.Stats(ctx, board)
	if err != nil {
		return Dashboard{}, err
	}
	d := Dashboard{Boards: boards, Stats: st}
	for i := range tasks {
		t := &tasks[i]
		d.Tasks = append(d.Tasks, DashboardTask{
			ID: t.ID, BoardID: t.BoardID, Title: t.Title, Status: t.Status, Assignee: t.Assignee,
			Priority: t.Priority, Archived: t.Archived, UpdatedAt: t.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Attachments: len(t.Attachments), Comments: t.Comments,
		})
		d.Runs = append(d.Runs, t.Runs...)
	}
	return d, nil
}

// UnitOptions describes the generated systemd unit. Environment file paths
// are intentionally not a field: the unit must not carry a secret path.
type UnitOptions struct {
	Binary string
	Data   string
	User   string
}

// SystemdUnit renders a user service for the board plugins. The output is
// deterministic, contains only the binary and data dir, and refuses a secret
// marker in either path.
func SystemdUnit(o UnitOptions) (string, error) {
	if o.Binary == "" {
		o.Binary = "/usr/local/bin/damnbot"
	}
	if o.Data == "" {
		o.Data = "%h/.local/share/damnbot"
	}
	for _, v := range []string{o.Binary, o.Data, o.User} {
		low := strings.ToLower(v)
		if strings.Contains(low, "secret") || strings.Contains(low, "token") || strings.Contains(low, "password") {
			return "", fmt.Errorf("%w: unit path must not name a secret", ErrInvalid)
		}
	}
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=damnbot kanban dispatcher\nAfter=network-online.target\n\n")
	b.WriteString("[Service]\nType=simple\n")
	b.WriteString("ExecStart=" + o.Binary + " kanban daemon --data-dir " + o.Data + "\n")
	if o.User != "" {
		b.WriteString("User=" + o.User + "\n")
	}
	b.WriteString("Restart=on-failure\nRestartSec=15\n\n[Install]\nWantedBy=default.target\n")
	return b.String(), nil
}
