package importer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/ctxdb/importstore"
)

// MaxSessionMessageBytes clamps one message; the clamp is reported.
const MaxSessionMessageBytes = 1 << 20

// MaxSessionMessages is the per-session message cap of the source reader.
const MaxSessionMessages = importstore.MaxMessages

// Session is one Hermes session with its messages in source order.
type Session = importstore.HermesSession

// SessionMessage is one source transcript row; role is one of user, assistant,
// tool or system, and Timestamp is Unix milliseconds (0 when absent).
type SessionMessage = importstore.HermesMessage

// SessionSource reads one Hermes state.db read-only. It must not modify the
// source file or its WAL/SHM siblings.
type SessionSource interface {
	ReadSessions(ctx context.Context, path string) ([]Session, error)
}

func (i *Importer) collectSessions(ctx context.Context, root string, c *collected) error {
	if reason, ok := c.disabled[SourceSessions]; ok {
		c.report.skip(SourceSessions, "(all)", reason)
		return nil
	}
	sessions, err := i.deps.Sessions.ReadSessions(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		return fmt.Errorf("state.db: %w", err)
	}
	for index := range sessions {
		s := sessions[index]
		s.SourceID = strings.TrimSpace(s.SourceID)
		if s.SourceID == "" {
			c.report.skip(SourceSessions, "(empty id)", "session has no id")
			continue
		}
		if s.Skipped > 0 {
			c.report.skip(SourceSessions, s.SourceID,
				fmt.Sprintf("%d messages beyond the %d-message cap", s.Skipped, importstore.MaxMessages))
		}
		c.sessions = append(c.sessions, normalizeSession(&s, &c.report))
		c.report.item(SourceSessions)
	}
	return nil
}

func normalizeSession(s *Session, report *Report) Session {
	out := make([]SessionMessage, 0, len(s.Messages))
	for _, m := range s.Messages {
		m.Role = strings.ToLower(strings.TrimSpace(m.Role))
		switch m.Role {
		case "user", "assistant", "tool", "system":
		default:
			report.skip(SourceSessions, sessionKey(s.SourceID, m.SourceID),
				fmt.Sprintf("unknown role %q", m.Role))
			continue
		}
		if len(m.Content) > MaxSessionMessageBytes {
			report.Clamps = append(report.Clamps, Clamp{
				Key:  sessionKey(s.SourceID, m.SourceID),
				From: len(m.Content),
				To:   len(clampUTF8(m.Content, MaxSessionMessageBytes)),
			})
			m.Content = clampUTF8(m.Content, MaxSessionMessageBytes)
		}
		m.ToolName = strings.TrimSpace(m.ToolName)
		out = append(out, m)
	}
	s.Messages = out
	if s.Title == "" {
		s.Title = firstUserLine(out)
	}
	return *s
}

func firstUserLine(msgs []SessionMessage) string {
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		line, _, _ := strings.Cut(strings.TrimSpace(m.Content), "\n")
		r := []rune(strings.TrimSpace(line))
		if len(r) > 80 {
			r = r[:80]
		}
		if len(r) > 0 {
			return string(r)
		}
	}
	return ""
}

func sessionKey(sessionID string, messageID int64) string {
	return fmt.Sprintf("%s/message:%d", sessionID, messageID)
}

func clampUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
