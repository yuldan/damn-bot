package e2e

// Scripted conversation simulator for the fake Telegram Bot API. A
// Conversation owns the fake server, tracks the getUpdates cursor the way a
// real bot does, and offers small send/edit/answer helpers. RunConversation
// executes inject → assert steps serially with a 5 s budget per step and logs
// the last ten outbound sends on failure (never the token).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Conversation is a fake server plus the getUpdates offset a caller keeps.
type Conversation struct {
	Server *FakeTG
	Offset int64
}

// NewConversation starts a fake server behind a conversation simulator.
func NewConversation(tb testing.TB, opts *FakeTGOptions) *Conversation {
	tb.Helper()
	return &Conversation{Server: NewFakeTG(tb, *opts)}
}

// Next fetches the pending updates with timeout=0, decodes them and advances
// the stored offset past every delivered update.
func (c *Conversation) Next(ctx context.Context) ([]FakeTGUpdate, error) {
	form := url.Values{"offset": {strconv.FormatInt(c.Offset, 10)}, "timeout": {"0"}}
	_, body, err := c.Server.Call(ctx, "getUpdates", form)
	if err != nil {
		return nil, fmt.Errorf("conversation: getUpdates: %w", err)
	}
	updates, err := decodeTGValue[[]FakeTGUpdate](body)
	if err != nil {
		return nil, fmt.Errorf("conversation: getUpdates: %w", err)
	}
	for _, update := range updates {
		if update.UpdateID >= c.Offset {
			c.Offset = update.UpdateID + 1
		}
	}
	return updates, nil
}

// Send posts a text message and returns the created message.
func (c *Conversation) Send(ctx context.Context, chatID int64, text string) (FakeTGMessage, error) {
	form := url.Values{"chat_id": {strconv.FormatInt(chatID, 10)}, "text": {text}}
	_, body, err := c.Server.Call(ctx, "sendMessage", form)
	if err != nil {
		return FakeTGMessage{}, fmt.Errorf("conversation: sendMessage: %w", err)
	}
	msg, err := decodeTGValue[FakeTGMessage](body)
	if err != nil {
		return FakeTGMessage{}, fmt.Errorf("conversation: sendMessage: %w", err)
	}
	return msg, nil
}

// Edit posts editMessageText for an existing message.
func (c *Conversation) Edit(ctx context.Context, chatID, messageID int64, text string) (FakeTGMessage, error) {
	form := url.Values{
		"chat_id":    {strconv.FormatInt(chatID, 10)},
		"message_id": {strconv.FormatInt(messageID, 10)},
		"text":       {text},
	}
	_, body, err := c.Server.Call(ctx, "editMessageText", form)
	if err != nil {
		return FakeTGMessage{}, fmt.Errorf("conversation: editMessageText: %w", err)
	}
	msg, err := decodeTGValue[FakeTGMessage](body)
	if err != nil {
		return FakeTGMessage{}, fmt.Errorf("conversation: editMessageText: %w", err)
	}
	return msg, nil
}

// AnswerCallback posts answerCallbackQuery for one callback query id.
func (c *Conversation) AnswerCallback(ctx context.Context, callbackQueryID string) error {
	form := url.Values{"callback_query_id": {callbackQueryID}}
	_, body, err := c.Server.Call(ctx, "answerCallbackQuery", form)
	if err != nil {
		return fmt.Errorf("conversation: answerCallbackQuery: %w", err)
	}
	if _, err := decodeTGValue[bool](body); err != nil {
		return fmt.Errorf("conversation: answerCallbackQuery: %w", err)
	}
	return nil
}

// ConversationStep is one inject or assert step.
type ConversationStep struct {
	Name string
	Run  func(ctx context.Context, c *Conversation) error
}

// ConversationScript names a scenario and lists its serial steps.
type ConversationScript struct {
	Name  string
	Conv  *Conversation
	Steps []ConversationStep
}

// ConversationStepTimeout is the per-step wall-clock budget.
const ConversationStepTimeout = 5 * time.Second

// RunConversation executes the steps serially. Each step runs in its own
// goroutine so the runner can fail at 5 s instead of hanging the suite.
func RunConversation(t *testing.T, script ConversationScript) {
	t.Helper()
	if script.Conv == nil || script.Conv.Server == nil {
		t.Fatal("conversation: script has no server")
	}
	t.Run(script.Name, func(t *testing.T) {
		for _, step := range script.Steps {
			t.Run(step.Name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), ConversationStepTimeout)
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- step.Run(ctx, script.Conv) }()
				select {
				case err := <-result:
					if err != nil {
						t.Fatalf("conversation %q step %q: %v\nlast sends:\n%s",
							script.Name, step.Name, err, lastSendsLog(script.Conv.Server, 10))
					}
				case <-ctx.Done():
					t.Fatalf("conversation %q step %q: timeout after %s\nlast sends:\n%s",
						script.Name, step.Name, ConversationStepTimeout, lastSendsLog(script.Conv.Server, 10))
				}
			})
		}
	})
}

func lastSendsLog(s *FakeTG, limit int) string {
	sends := s.Sends()
	if len(sends) > limit {
		sends = sends[len(sends)-limit:]
	}
	if len(sends) == 0 {
		return "  (none)"
	}
	var b strings.Builder
	for i := range sends {
		fmt.Fprintf(&b, "  chat=%d message_id=%d text=%q\n", sends[i].ChatID, sends[i].ResultMessageID, sends[i].Text)
	}
	return b.String()
}

type tgResultEnvelope[T any] struct {
	OK          bool          `json:"ok"`
	Result      T             `json:"result"`
	ErrorCode   int           `json:"error_code,omitempty"`
	Description string        `json:"description,omitempty"`
	Parameters  *tgParameters `json:"parameters,omitempty"`
}

func decodeTGValue[T any](body []byte) (T, error) {
	var env tgResultEnvelope[T]
	if err := json.Unmarshal(body, &env); err != nil {
		var zero T
		return zero, fmt.Errorf("decode envelope: %w", err)
	}
	if !env.OK {
		var zero T
		out := FakeTGAPIError{Code: env.ErrorCode, Description: env.Description}
		if env.Parameters != nil {
			out.RetryAfter = env.Parameters.RetryAfter
		}
		return zero, out
	}
	return env.Result, nil
}
