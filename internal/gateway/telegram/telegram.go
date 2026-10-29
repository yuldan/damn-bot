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

	"github.com/damnbot/damnbot/internal/gateway/render"
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

// Config builds an [Adapter]. Account is the bot id (default: numeric token
// prefix); Username (without "@") is required to attribute "@bot" mentions in
// groups, otherwise they are ignored (fail-closed, EC-PLAT-022).
type Config struct {
	Token, BaseURL, Account, Username string
	Doer                              Doer
	Store                             Store
	Clock                             Clock
	// NoCompression disables EC-PLAT-023 image compression: an image above
	// the photo limit is sent unchanged as a document instead.
	NoCompression bool
}

// Adapter is one configured Telegram bot.
type Adapter struct {
	base, token string
	account     string
	username    string
	botID       int64
	doer        Doer
	store       Store
	clock       Clock
	send        *limiter
	edit        *limiter
	noCompress  bool
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
	account := cfg.Account
	if account == "" {
		if id, _, ok := strings.Cut(cfg.Token, ":"); ok {
			account = id
		}
	}
	a := &Adapter{
		base: base, token: cfg.Token, doer: cfg.Doer, store: cfg.Store,
		clock: clock, after: 300 * time.Second, pending: map[string]*approval{},
		account: account, username: strings.TrimPrefix(cfg.Username, "@"),
		noCompress: cfg.NoCompression,
	}
	if id, err := strconv.ParseInt(account, 10, 64); err == nil {
		a.botID = id
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
	return a.post(ctx, method, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", out)
}

// post sends one Bot API request and decodes the envelope. It is shared by the
// form and multipart paths so the token redaction and response limit apply to
// every call.
func (a *Adapter) post(ctx context.Context, method string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/bot"+a.token+"/"+method, body)
	if err != nil {
		return fmt.Errorf("telegram: %s: request", method)
	}
	req.Header.Set("Content-Type", contentType)
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
	MessageID       int64      `json:"message_id"`
	From            *tgID      `json:"from"`
	Chat            tgID       `json:"chat"`
	Text            string     `json:"text"`
	MessageThreadID int64      `json:"message_thread_id"`
	Entities        []tgEntity `json:"entities"`
	ReplyTo         *tgMessage `json:"reply_to_message"`
}
type tgID struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
	Type     string `json:"type"`
}
type tgEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *tgID  `json:"user"`
}
type callbackQuery struct {
	ID      string     `json:"id"`
	Data    string     `json:"data"`
	Message *tgMessage `json:"message"`
}

// Inbound is one authenticated update handed to the turn layer; the boolean
// fields are the mention-policy facts (GAP-GW-05, EC-PLAT-022).
type Inbound struct {
	Platform, Account, ChatID, ThreadID, SenderID, UpdateID, Text string
	Group, Mentioned, ReplyToBot, Command, SenderIsBot            bool
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

// sendMessage sends one rendered message. No parse_mode is ever set: markup
// travels as Bot API entities over the escaped plain text (GAP-GW-04). anchor
// is an already validated reply_to_message_id, empty to omit it.
func (a *Adapter) sendMessage(ctx context.Context, chatID, threadID, anchor string, msg render.Message, keyboard string) (int64, error) {
	form := url.Values{"chat_id": {chatID}, "text": {msg.Text}}
	if threadID != "" {
		form.Set("message_thread_id", threadID)
	}
	if anchor != "" {
		form.Set("reply_to_message_id", anchor)
	}
	if err := setEntities(form, msg); err != nil {
		return 0, err
	}
	if keyboard != "" {
		form.Set("reply_markup", keyboard)
	}
	var sent tgMessage
	if err := a.call(ctx, "sendMessage", form, &sent); err != nil {
		return 0, err
	}
	return sent.MessageID, nil
}

// editText edits one rendered message in place, again without parse_mode.
func (a *Adapter) editText(ctx context.Context, chatID, threadID string, messageID int64, msg render.Message) error {
	form := url.Values{"chat_id": {chatID}, "text": {msg.Text}, "message_id": {strconv.FormatInt(messageID, 10)}}
	if threadID != "" {
		form.Set("message_thread_id", threadID)
	}
	if err := setEntities(form, msg); err != nil {
		return err
	}
	return a.call(ctx, "editMessageText", form, nil)
}

// setEntities adds the Bot API entities array; offsets are UTF-16 units as the
// renderer produced them.
func setEntities(form url.Values, msg render.Message) error {
	if len(msg.Entities) == 0 {
		return nil
	}
	raw, err := json.Marshal(msg.Entities)
	if err != nil {
		return fmt.Errorf("telegram: encode entities: %w", err)
	}
	form.Set("entities", string(raw))
	return nil
}
