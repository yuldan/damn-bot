package telegram

import (
	"context"
	"crypto/rand"
	"errors"
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
// callback.
func (a *Adapter) Approve(ctx context.Context, chatID, threadID, text string) (Decision, error) {
	id, err := a.OpenApproval(ctx, chatID, threadID, text)
	if err != nil {
		return DecisionDeny, err
	}
	return a.AwaitApproval(ctx, chatID, id)
}

// OpenApproval sends the approval card and returns its id. The card carries the
// id, so an owner can answer it with /approve or /deny as well as the buttons.
func (a *Adapter) OpenApproval(ctx context.Context, chatID, threadID, text string) (string, error) {
	return a.openApproval(ctx, chatID, threadID, text, rand.Text()[:16])
}

// ApproveNonce issues the card with a caller-chosen nonce so the durable
// approval ledger can persist its hash (GAP-SEC-13). The nonce is still
// one-time: replaying it is rejected by the durable claim.
func (a *Adapter) ApproveNonce(ctx context.Context, chatID, threadID, text, nonce string) (Decision, error) {
	if nonce == "" {
		return DecisionDeny, errors.New("telegram: approval nonce is required")
	}
	if _, err := a.OpenApprovalWithNonce(ctx, chatID, threadID, text, nonce); err != nil {
		return DecisionDeny, err
	}
	return a.AwaitApproval(ctx, chatID, nonce)
}

// OpenApprovalWithNonce sends the card under a caller-chosen nonce and returns
// it, so the durable ledger can register the id before awaiting the answer.
func (a *Adapter) OpenApprovalWithNonce(ctx context.Context, chatID, threadID, text, nonce string) (string, error) {
	if nonce == "" {
		return "", errors.New("telegram: approval nonce is required")
	}
	return a.openApproval(ctx, chatID, threadID, text, nonce)
}

func (a *Adapter) openApproval(ctx context.Context, chatID, threadID, text, nonce string) (string, error) {
	a.mu.Lock()
	a.pending[nonce] = &approval{chatID: chatID, decision: make(chan Decision, 1)}
	a.mu.Unlock()
	keyboard := `{"inline_keyboard":[[{"text":"Allow once","callback_data":"` + callbackPrefix + nonce + `:allow"},{"text":"Deny","callback_data":"` + callbackPrefix + nonce + `:deny"}]]}`
	if err := a.send.wait(ctx, chatID); err != nil {
		a.dropApproval(nonce)
		return "", err
	}
	card := approvalCard(text + "\n\napproval: " + nonce)
	if _, err := a.sendMessage(ctx, chatID, threadID, "", card, keyboard); err != nil {
		a.dropApproval(nonce)
		return "", err
	}
	return nonce, nil
}

// AwaitApproval waits for either the callback or an explicit Decision.
func (a *Adapter) AwaitApproval(ctx context.Context, chatID, id string) (Decision, error) {
	a.mu.Lock()
	p := a.pending[id]
	a.mu.Unlock()
	if p == nil || p.chatID != chatID {
		return DecisionDeny, ErrApprovalGone
	}
	defer a.dropApproval(id)
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

// DecideApproval answers a pending approval by text command. It returns false
// when the id is unknown, another chat's, or already answered.
func (a *Adapter) DecideApproval(chatID, id string, allow bool) bool {
	a.mu.Lock()
	p := a.pending[id]
	a.mu.Unlock()
	if p == nil || p.chatID != chatID {
		return false
	}
	d := DecisionDeny
	if allow {
		d = DecisionAllow
	}
	select {
	case p.decision <- d:
		return true
	default:
		return false
	}
}

// ErrApprovalGone means the card expired or another path already answered it.
var ErrApprovalGone = errors.New("telegram: approval is no longer pending")

func (a *Adapter) dropApproval(id string) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
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
	// GAP-SEC-13: the nonce is one-time and the claim is durable, so a
	// replayed button (new callback query id, same nonce) is rejected even
	// across a restart where the prompt was re-issued to another adapter.
	claimed, err := a.store.ClaimCallback(ctx, nonceClaim(nonce))
	if err != nil {
		return fmt.Errorf("telegram: claim approval nonce: %w", err)
	}
	if !claimed {
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

// nonceClaim namespaces the durable one-time claim so it can never collide
// with a Telegram callback query id in the same table.
func nonceClaim(nonce string) string { return "approval:" + nonce }

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
