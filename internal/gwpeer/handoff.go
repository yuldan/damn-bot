package gwpeer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// Handoff errors.
var (
	ErrInvalidPlatform = errors.New("gwpeer: invalid handoff platform")
	ErrHandoffFailed   = errors.New("gwpeer: handoff delivery failed")
)

// TranscriptLine is one compact line of the CLI session handed to a chat
// platform.
type TranscriptLine struct {
	Role string
	Text string
}

// HandoffRequest asks to move one CLI session to platform's chat.
type HandoffRequest struct {
	SessionID  string
	Platform   string
	ChatID     string
	Transcript []TranscriptLine
}

// HandoffRecord is what the session store records for an accepted handoff.
type HandoffRecord struct {
	SessionID string
	Platform  string
	ChatID    string
	MessageID string
	At        time.Time
}

// HandoffStore records a completed handoff on the session. It never decides
// whether the handoff is allowed.
type HandoffStore interface {
	RecordHandoff(ctx context.Context, record *HandoffRecord) error
}

// HandoffAdapter delivers the summary to one platform.
type HandoffAdapter interface {
	SendHandoff(ctx context.Context, sessionID, chatID, text string) (messageID string, err error)
}

// HandoffResult reports the outcome. Sent=false is the "no active adapter"
// answer: the caller shows Message and starts no turn.
type HandoffResult struct {
	Platform  string
	MessageID string
	Summary   string
	Sent      bool
	Repeat    bool
	Message   string
}

// Handoff moves a CLI session to a chat platform (GW-026) and backs the
// `/handoff` slash command (SLASH-012). A retry with the same session,
// platform, and transcript returns the first message id and sends nothing
// twice.
type Handoff struct {
	Adapters map[string]HandoffAdapter
	Store    HandoffStore
	Now      func() time.Time
	// Transcript returns the session's compact history for the slash path.
	Transcript func(ctx context.Context, sessionID string) ([]TranscriptLine, error)

	mu   sync.Mutex
	done map[string]HandoffResult
}

// DefaultSummaryLimit bounds the compact transcript.
const DefaultSummaryLimit = 2_000

// Execute validates the platform and either sends the summary or reports the
// missing adapter. It never starts an LLM turn.
func (h *Handoff) Execute(ctx context.Context, req *HandoffRequest) (HandoffResult, error) {
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	if !platformName(platform) || req.SessionID == "" {
		return HandoffResult{}, fmt.Errorf("%w: %q", ErrInvalidPlatform, req.Platform)
	}
	summary := Summarize(req.Transcript)
	key := handoffKey(req.SessionID, platform, req.ChatID, summary)
	if cached, ok := h.cached(key); ok {
		cached.Repeat = true
		return cached, nil
	}
	adapter := h.Adapters[platform]
	if adapter == nil {
		return HandoffResult{Platform: platform, Message: "platform " + platform + " is not active; no turn was started"}, nil
	}
	messageID, err := adapter.SendHandoff(ctx, req.SessionID, req.ChatID, summary)
	if err != nil {
		return HandoffResult{}, fmt.Errorf("%w: %w", ErrHandoffFailed, err)
	}
	result := HandoffResult{Platform: platform, MessageID: messageID, Summary: summary, Sent: true}
	if h.Store != nil {
		record := &HandoffRecord{SessionID: req.SessionID, Platform: platform, ChatID: req.ChatID, MessageID: messageID, At: h.now()}
		if err := h.Store.RecordHandoff(ctx, record); err != nil {
			return HandoffResult{}, fmt.Errorf("gwpeer: record handoff: %w", err)
		}
	}
	h.remember(key, result)
	return result, nil
}

// Slash handles `/handoff <platform>` as a session command: a valid platform
// with no active adapter answers with a message and Turn stays false.
func (h *Handoff) Slash(ctx context.Context, sessionID, chatID, text string) (gateway.CommandResult, bool) {
	cmd, ok := gateway.Parse(text)
	if !ok || cmd.Name != "handoff" {
		return gateway.CommandResult{}, false
	}
	if len(cmd.Args) != 1 {
		return gateway.CommandResult{Handled: true, Reply: "usage: /handoff <platform>"}, true
	}
	lines := []TranscriptLine(nil)
	if h.Transcript != nil {
		var err error
		lines, err = h.Transcript(ctx, sessionID)
		if err != nil {
			return gateway.CommandResult{Handled: true, Reply: "handoff: " + err.Error()}, true
		}
	}
	result, err := h.Execute(ctx, &HandoffRequest{
		SessionID: sessionID, Platform: cmd.Args[0], ChatID: chatID, Transcript: lines,
	})
	if err != nil {
		return gateway.CommandResult{Handled: true, Reply: "handoff: " + err.Error()}, true
	}
	if !result.Sent {
		return gateway.CommandResult{Handled: true, Reply: result.Message}, true
	}
	return gateway.CommandResult{Handled: true, Reply: "handed off to " + result.Platform + " (" + result.MessageID + ")"}, true
}

// Summarize renders a compact transcript, bounded by DefaultSummaryLimit.
func Summarize(lines []TranscriptLine) string {
	var b strings.Builder
	for _, line := range lines {
		entry := strings.TrimSpace(line.Role) + ": " + strings.TrimSpace(line.Text) + "\n"
		if b.Len()+len(entry) > DefaultSummaryLimit {
			remain := DefaultSummaryLimit - b.Len()
			if remain > 0 {
				b.WriteString(entry[:min(remain, len(entry))])
			}
			break
		}
		b.WriteString(entry)
	}
	return strings.TrimSpace(b.String())
}

func handoffKey(sessionID, platform, chatID, summary string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + platform + "\x00" + chatID + "\x00" + summary))
	return hex.EncodeToString(sum[:16])
}

func (h *Handoff) cached(key string) (HandoffResult, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	result, ok := h.done[key]
	return result, ok
}

func (h *Handoff) remember(key string, result HandoffResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done == nil {
		h.done = make(map[string]HandoffResult)
	}
	h.done[key] = result
}

func (h *Handoff) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func platformName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
