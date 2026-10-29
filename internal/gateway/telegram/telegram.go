// Package telegram is the Telegram Bot API adapter of the F4 gateway (SPEC
package telegram

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// ErrUncertain marks an attempt whose delivery outcome is unknown.
var ErrUncertain = errors.New("telegram: delivery outcome unknown")

// Doer is the one netx method the adapter needs; [netx.Client] implements it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Clock paces retries and throttled sends; tests inject a fake.
type Clock interface {
	Wait(ctx context.Context, d time.Duration) error
}

// Store is the adapter's durable state (ctxdb/gwstore); MessageID returns 0
type Store interface {
	Offset(ctx context.Context, adapter string) (int64, error)
	PutOffset(ctx context.Context, adapter string, next int64) error
	MessageID(ctx context.Context, key string) (int64, bool, error)
	PutMessageID(ctx context.Context, key string, id int64) error
	ClaimCallback(ctx context.Context, id string) (bool, error)
}

// Config builds an [Adapter]; Token comes from env/config, BaseURL is a
type Config struct {
	Token, BaseURL string
	Doer           Doer
	Store          Store
	Clock          Clock
}

// Adapter is one configured Telegram bot.
type Adapter struct {
	base, token string
	doer        Doer
	store       Store
	clock       Clock
	send        *limiter
	edit        *limiter
	after       time.Duration
	pending     map[string]*approval
	mu          sync.Mutex
}

// EgressClient returns the only netx client the gateway creates (ADR-247).
func EgressClient(net *netx.Network) *netx.Client { return net.Client("telegram", netx.Gateway) }

// New validates cfg and builds the adapter.
func New(cfg *Config) (*Adapter, error) {
	if cfg == nil || strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("telegram: bot token is required")
	}
	if cfg.Doer == nil || cfg.Store == nil {
		return nil, errors.New("telegram: doer and store are required")
	}
	base := strings.TrimRight(cmp.Or(cfg.BaseURL, "https://api.telegram.org"), "/")
	clock := cfg.Clock
	if clock == nil {
		clock = realClock{}
	}
	a := &Adapter{
		base: base, token: cfg.Token, doer: cfg.Doer, store: cfg.Store,
		clock: clock, after: 300 * time.Second, pending: map[string]*approval{},
	}
	a.send = &limiter{clock: clock, per: time.Second, next: map[string]time.Time{}}
	a.edit = &limiter{clock: clock, per: 1500 * time.Millisecond, next: map[string]time.Time{}}
	return a, nil
}

type apiError struct {
	Method, Description string
	Code                int
	RetryAfter          time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram: %s: %d %s", e.Method, e.Code, e.Description)
}

func (a *Adapter) call(ctx context.Context, method string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/bot"+a.token+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram: %s: request", method)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.doer.Do(req)
	if err != nil {
		if s := a.redact(err.Error()); s != err.Error() {
			err = errors.New(s)
		}
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	defer resp.Body.Close() //nolint:errcheck // body is read or discarded
	var env envelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&env); err != nil {
		return &apiError{Method: method, Code: resp.StatusCode, Description: "malformed response"}
	}
	if !env.OK {
		e := &apiError{Method: method, Code: env.ErrorCode, Description: a.redact(env.Description)}
		if e.Code == 0 {
			e.Code = resp.StatusCode
		}
		e.RetryAfter = time.Duration(env.Parameters["retry_after"]) * time.Second
		return e
	}
	if out == nil || len(env.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return &apiError{Method: method, Code: resp.StatusCode, Description: "malformed result"}
	}
	return nil
}

func (a *Adapter) redact(s string) string {
	if a.token == "" {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "bot"+a.token, "[redacted]"), a.token, "[redacted]")
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  map[string]int  `json:"parameters"`
}
type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *tgMessage     `json:"message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}
type tgMessage struct {
	MessageID       int64  `json:"message_id"`
	From            *tgID  `json:"from"`
	Chat            tgID   `json:"chat"`
	Text            string `json:"text"`
	MessageThreadID int64  `json:"message_thread_id"`
}
type tgID struct {
	ID int64 `json:"id"`
}
type callbackQuery struct {
	ID      string     `json:"id"`
	Data    string     `json:"data"`
	Message *tgMessage `json:"message"`
}

// Inbound is one authenticated update handed to the turn layer. Text is
type Inbound struct {
	Platform, ChatID, ThreadID, SenderID, UpdateID, Text string
}
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("telegram: clock: %w", ctx.Err())
	}
}

type limiter struct {
	clock  Clock
	per    time.Duration
	mu     sync.Mutex
	next   map[string]time.Time
	nextAt time.Time
}

func (l *limiter) wait(ctx context.Context, key string) error {
	l.mu.Lock()
	now := time.Now()
	at := now
	if t := l.next[key]; t.After(at) {
		at = t
	}
	if l.nextAt.After(at) {
		at = l.nextAt
	}
	l.next[key], l.nextAt = at.Add(l.per), at.Add(time.Second/30)
	l.mu.Unlock()
	if err := l.clock.Wait(ctx, at.Sub(now)); err != nil {
		return fmt.Errorf("telegram: pace: %w", err)
	}
	return nil
}

func (a *Adapter) sendMessage(ctx context.Context, chatID, threadID, text, keyboard string) (int64, error) {
	form := url.Values{"chat_id": {chatID}, "text": {text}}
	if threadID != "" {
		form.Set("message_thread_id", threadID)
	}
	if keyboard != "" {
		form.Set("reply_markup", keyboard)
	}
	var msg tgMessage
	if err := a.call(ctx, "sendMessage", form, &msg); err != nil {
		return 0, err
	}
	return msg.MessageID, nil
}

func (a *Adapter) editText(ctx context.Context, chatID, threadID string, messageID int64, text string) error {
	form := url.Values{"chat_id": {chatID}, "text": {text}, "message_id": {strconv.FormatInt(messageID, 10)}}
	if threadID != "" {
		form.Set("message_thread_id", threadID)
	}
	return a.call(ctx, "editMessageText", form, nil)
}
