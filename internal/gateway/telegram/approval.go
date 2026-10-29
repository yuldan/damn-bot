package telegram

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/gateway/render"
)

// Decision is an inline-keyboard approval outcome; anything but an explicit
type Decision string

// Approval decisions.
const (
	DecisionAllow, DecisionDeny Decision = "allow", "deny" //nolint:revive // paired decisions documented above
	callbackPrefix                       = "approve:"
)

type approval struct {
	chatID   string
	decision chan Decision
}

// Approve sends the preview with allow-once/deny buttons and waits for the
func (a *Adapter) Approve(ctx context.Context, chatID, threadID, text string) (Decision, error) {
	nonce := rand.Text()[:16]
	p := &approval{chatID: chatID, decision: make(chan Decision, 1)}
	a.mu.Lock()
	a.pending[nonce] = p
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, nonce)
		a.mu.Unlock()
	}()
	keyboard := `{"inline_keyboard":[[{"text":"Allow once","callback_data":"` + callbackPrefix + nonce + `:allow"},{"text":"Deny","callback_data":"` + callbackPrefix + nonce + `:deny"}]]}`
	if err := a.send.wait(ctx, chatID); err != nil {
		return DecisionDeny, err
	}
	if _, err := a.sendMessage(ctx, chatID, threadID, "", approvalCard(text), keyboard); err != nil {
		return DecisionDeny, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, a.after)
	defer cancel()
	select {
	case d := <-p.decision:
		return d, nil
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return DecisionDeny, fmt.Errorf("telegram: approval: %w", ctx.Err())
		}
		return DecisionDeny, nil
	}
}

// handleCallback answers one callback query exactly once and applies its
func (a *Adapter) handleCallback(ctx context.Context, q *callbackQuery) error {
	if q.ID == "" {
		return nil
	}
	first, err := a.store.ClaimCallback(ctx, q.ID)
	if err != nil {
		return fmt.Errorf("telegram: claim callback: %w", err)
	}
	if !first {
		return nil
	}
	nonce, d, ok := parseCallback(q.Data)
	a.mu.Lock()
	p := a.pending[nonce]
	a.mu.Unlock()
	if !ok || p == nil || q.Message == nil || p.chatID != strconv.FormatInt(q.Message.Chat.ID, 10) {
		return a.call(ctx, "answerCallbackQuery", url.Values{"callback_query_id": {q.ID}, "text": {"request expired"}}, nil)
	}
	if err := a.call(ctx, "answerCallbackQuery", url.Values{"callback_query_id": {q.ID}, "text": {"decided"}}, nil); err != nil {
		return err
	}
	select {
	case p.decision <- d:
	default: // already decided
	}
	return nil
}

func parseCallback(data string) (string, Decision, bool) {
	rest, ok := strings.CutPrefix(data, callbackPrefix)
	if !ok {
		return "", DecisionDeny, false
	}
	nonce, action, ok := strings.Cut(rest, ":")
	if !ok || nonce == "" || (action != string(DecisionAllow) && action != string(DecisionDeny)) {
		return "", DecisionDeny, false
	}
	return nonce, Decision(action), true
}

// approvalCard renders the preview with the same renderer as replies and keeps
// it to one message because the keyboard lives on that message. MeasureCard
// reports the escaped UTF-16 length; when it exceeds MaxLen the card is cut
// with the [truncated] marker (EC-PLAT-012).
func approvalCard(text string) render.Message {
	limit := min(render.MeasureCard(text), render.MaxLen)
	msgs := render.RenderTelegram(text, render.Limits{MaxLen: limit, MaxChunks: 1})
	if len(msgs) == 0 {
		return render.Message{Text: render.Escape(text)}
	}
	return msgs[0]
}
