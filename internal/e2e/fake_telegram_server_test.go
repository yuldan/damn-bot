package e2e

// The fake Telegram Bot API of F4: a loopback httptest server that mimics the
// subset the gateway needs (getUpdates long polling, message CRUD, callbacks,
// commands, getMe and small media files), records every call in arrival order,
// and lets a test inject 429/5xx responses, delays, hung handlers and dropped
// connections. It is test-only: every symbol here lives in a _test.go file and
// is never linked into the release binary.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// FakeTGToken is the token the fake accepts when Options.Token is empty.
const FakeTGToken = "424242:fake-telegram-token"

// FakeTGOptions configures a FakeTG. The zero value is a healthy server with
// the default token and bot identity.
type FakeTGOptions struct {
	Token  string                   // accepted /bot<token>/… token
	Bot    FakeTGUser               // identity returned by getMe
	Faults map[string]FakeTGFault   // per-method fault injection
	Delays map[string]time.Duration // per-method latency before responding
	Files  map[string]FakeTGFile    // file_id → media served by getFile/get
}

// FakeTGFault injects failures for one Bot API method. Every and Times select
// which calls are hit: Every=2 hits the 2nd, 4th, … matching call, while
// Times stops after that many faulted calls (zero means no limit). Close drops
// the TCP connection, Drop holds the handler until the request context ends,
// and Status (with RetryAfter for 429) replies with a Telegram error envelope.
type FakeTGFault struct {
	Status     int
	RetryAfter int
	Every      int
	Times      int
	Drop       bool
	Close      bool
}

// FakeTGFile is one small media file exposed through getFile and /file/.
type FakeTGFile struct {
	Path string // download path below the token, e.g. documents/note.txt
	Data []byte
}

// FakeTGUser is a Telegram user/bot object.
type FakeTGUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// FakeTGChat is a Telegram chat object.
type FakeTGChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type,omitempty"`
}

// FakeTGMessage is a Telegram message object.
type FakeTGMessage struct {
	MessageID       int64                 `json:"message_id"`
	MessageThreadID int                   `json:"message_thread_id,omitempty"`
	From            *FakeTGUser           `json:"from,omitempty"`
	Date            int64                 `json:"date"`
	EditDate        int64                 `json:"edit_date,omitempty"`
	Chat            FakeTGChat            `json:"chat"`
	Text            string                `json:"text,omitempty"`
	Caption         string                `json:"caption,omitempty"`
	Entities        []FakeTGMessageEntity `json:"entities,omitempty"`
	Photo           []FakeTGPhotoSize     `json:"photo,omitempty"`
	Document        *FakeTGDocument       `json:"document,omitempty"`
}

// FakeTGMessageEntity is one message entity (only bot_command is produced).
type FakeTGMessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// FakeTGPhotoSize is one inbound photo size.
type FakeTGPhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

// FakeTGDocument is one inbound document.
type FakeTGDocument struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// FakeTGMedia describes an inbound media message for PushMedia.
type FakeTGMedia struct {
	Kind    string // "photo" or any document mime type / label
	FileID  string
	Caption string
}

// FakeTGCallbackQuery is a Telegram callback query update payload.
type FakeTGCallbackQuery struct {
	ID      string         `json:"id"`
	From    FakeTGUser     `json:"from"`
	Data    string         `json:"data,omitempty"`
	Message *FakeTGMessage `json:"message,omitempty"`
}

// FakeTGUpdate is one getUpdates result element.
type FakeTGUpdate struct {
	UpdateID      int64                `json:"update_id"`
	Message       *FakeTGMessage       `json:"message,omitempty"`
	EditedMessage *FakeTGMessage       `json:"edited_message,omitempty"`
	CallbackQuery *FakeTGCallbackQuery `json:"callback_query,omitempty"`
}

// FakeTGRequest is one recorded Bot API call, in arrival order.
type FakeTGRequest struct {
	Seq             int
	Method          string
	ChatID          int64
	MessageID       int64 // message_id parameter (edit/delete)
	ResultMessageID int64 // message_id returned by sendMessage/editMessageText
	Status          int   // HTTP status actually sent; 0 = dropped connection
	Text            string
	CallbackQueryID string
	CallbackData    string
	Offset          int64
	Timeout         int
	Limit           int
	Params          map[string]string // first value of every form field
}

// FakeTGAPIError is a decoded Telegram error envelope.
type FakeTGAPIError struct {
	Code        int
	Description string
	RetryAfter  int
}

// Error implements error so conversation drivers can branch on Telegram errors.
func (e FakeTGAPIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram %d: %s (retry_after %d)", e.Code, e.Description, e.RetryAfter)
	}
	return fmt.Sprintf("telegram %d: %s", e.Code, e.Description)
}

// FakeTGFileInfo is the getFile result.
type FakeTGFileInfo struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int    `json:"file_size"`
	FilePath     string `json:"file_path"`
}

// FakeTG is a running fake Telegram Bot API server.
type FakeTG struct {
	*httptest.Server

	token     string
	client    *http.Client
	bot       FakeTGUser
	files     map[string]FakeTGFile
	filePaths map[string]FakeTGFile
	faults    map[string]FakeTGFault
	delays    map[string]time.Duration

	mu            sync.Mutex
	calls         map[string]int
	faulted       map[string]int
	callbackData  map[string]string
	updates       []FakeTGUpdate
	nextUpdateID  int64
	nextMessageID int64
	notify        chan struct{}
	reqs          []FakeTGRequest
	seq           int
	active        int
}

// NewFakeTG starts the fake on a loopback listener and closes it when tb ends.
//
//nolint:gocritic // hugeParam: construction takes the options once; a pointer would only obscure call sites
func NewFakeTG(tb testing.TB, opts FakeTGOptions) *FakeTG {
	tb.Helper()
	token := opts.Token
	if token == "" {
		token = FakeTGToken
	}
	bot := opts.Bot
	if bot.ID == 0 {
		bot = FakeTGUser{ID: 4242, IsBot: true, FirstName: "fake", Username: "fake_bot"}
	}
	files := maps.Clone(opts.Files)
	paths := make(map[string]FakeTGFile, len(files))
	for id, f := range files {
		if f.Path == "" {
			f.Path = "documents/" + id
			files[id] = f
		}
		paths[f.Path] = f
	}
	s := &FakeTG{
		token:        token,
		bot:          bot,
		files:        files,
		filePaths:    paths,
		faults:       maps.Clone(opts.Faults),
		delays:       maps.Clone(opts.Delays),
		calls:        make(map[string]int),
		faulted:      make(map[string]int),
		callbackData: make(map[string]string),
		notify:       make(chan struct{}),
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	s.client = s.Client()
	tb.Cleanup(s.Close)
	return s
}

// MethodURL is the full /bot<token>/<method> URL.
func (s *FakeTG) MethodURL(method string) string {
	return s.URL + "/bot" + s.token + "/" + method
}

// FileURL is the full /file/bot<token>/<path> download URL.
func (s *FakeTG) FileURL(path string) string {
	return s.URL + "/file/bot" + s.token + "/" + path
}

// Call POSTs a form to method and returns status, body and transport error.
func (s *FakeTG) Call(ctx context.Context, method string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.MethodURL(method), strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, fmt.Errorf("fake telegram: build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(req)
}

// Get fetches target (usually FileURL) and returns status, body and error.
func (s *FakeTG) Get(ctx context.Context, target string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("fake telegram: build GET request: %w", err)
	}
	return s.do(req)
}

func (s *FakeTG) do(req *http.Request) (int, []byte, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("fake telegram: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // test helper; a read error takes precedence
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("fake telegram: read %s: %w", req.URL.Path, err)
	}
	return resp.StatusCode, body, nil
}

// Requests returns a deep copy of the recorded calls.
func (s *FakeTG) Requests() []FakeTGRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FakeTGRequest, len(s.reqs))
	for i := range s.reqs {
		out[i] = s.reqs[i]
		out[i].Params = maps.Clone(s.reqs[i].Params)
	}
	return out
}

// RequestsFor filters the recorded calls by method.
func (s *FakeTG) RequestsFor(method string) []FakeTGRequest {
	requests := s.Requests()
	out := make([]FakeTGRequest, 0)
	for i := range requests {
		if requests[i].Method == method {
			out = append(out, requests[i])
		}
	}
	return out
}

// Sends returns successful sendMessage calls in order. Faulted attempts stay
// visible through RequestsFor("sendMessage").
func (s *FakeTG) Sends() []FakeTGRequest { return s.successful("sendMessage") }

// Edits returns successful editMessageText calls in order; a streaming turn
// may legitimately contain many edits for one message.
func (s *FakeTG) Edits() []FakeTGRequest { return s.successful("editMessageText") }

// EditsFor returns successful edits for one message id.
func (s *FakeTG) EditsFor(messageID int64) []FakeTGRequest {
	edits := s.Edits()
	out := make([]FakeTGRequest, 0)
	for i := range edits {
		if edits[i].MessageID == messageID {
			out = append(out, edits[i])
		}
	}
	return out
}

// CallbackAnswers returns successful answerCallbackQuery calls in order.
func (s *FakeTG) CallbackAnswers() []FakeTGRequest { return s.successful("answerCallbackQuery") }

func (s *FakeTG) successful(method string) []FakeTGRequest {
	requests := s.RequestsFor(method)
	out := make([]FakeTGRequest, 0)
	for i := range requests {
		if requests[i].Status == http.StatusOK {
			out = append(out, requests[i])
		}
	}
	return out
}

// WaitSend blocks until a successful send to chatID matches pred (nil = any).
func (s *FakeTG) WaitSend(chatID int64, pred func(FakeTGRequest) bool, timeout time.Duration) (FakeTGRequest, error) {
	return s.waitOutbound("send", chatID, pred, timeout, s.Sends)
}

// WaitEdit blocks until a successful edit to chatID matches pred (nil = any).
func (s *FakeTG) WaitEdit(chatID int64, pred func(FakeTGRequest) bool, timeout time.Duration) (FakeTGRequest, error) {
	return s.waitOutbound("edit", chatID, pred, timeout, s.Edits)
}

func (s *FakeTG) waitOutbound(kind string, chatID int64, pred func(FakeTGRequest) bool, timeout time.Duration, list func() []FakeTGRequest) (FakeTGRequest, error) {
	deadline := time.Now().Add(timeout)
	for {
		requests := list()
		for i := range requests {
			req := &requests[i]
			if req.ChatID == chatID && (pred == nil || pred(*req)) {
				return *req, nil
			}
		}
		if time.Now().After(deadline) {
			return FakeTGRequest{}, fmt.Errorf("fake telegram: no matching %s to chat %d within %s", kind, chatID, timeout)
		}
		timer := time.NewTimer(5 * time.Millisecond)
		<-timer.C
	}
}

// Drain waits (default 2s) until no handler is in flight and the request count
// is stable, so assertions see every response status and result id.
func (s *FakeTG) Drain(timeouts ...time.Duration) error {
	timeout := 2 * time.Second
	if len(timeouts) > 0 && timeouts[0] > 0 {
		timeout = timeouts[0]
	}
	deadline := time.Now().Add(timeout)
	last := -1
	stableSince := time.Now()
	for {
		total := len(s.Requests())
		active := s.Active()
		if total != last || active != 0 {
			last = total
			stableSince = time.Now()
		} else if time.Since(stableSince) >= 10*time.Millisecond {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("fake telegram: outbound not stable after %s (%d requests, %d active)", timeout, total, active)
		}
		timer := time.NewTimer(2 * time.Millisecond)
		<-timer.C
	}
}

// PushUpdate appends an update and wakes every blocked getUpdates call. A zero
// UpdateID is assigned automatically; the effective id is returned.
func (s *FakeTG) PushUpdate(up FakeTGUpdate) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if up.UpdateID <= 0 {
		s.nextUpdateID++
		up.UpdateID = s.nextUpdateID
	} else if up.UpdateID > s.nextUpdateID {
		s.nextUpdateID = up.UpdateID
	}
	s.updates = append(s.updates, up)
	close(s.notify)
	s.notify = make(chan struct{})
	return up.UpdateID
}

// PushText queues a plain text message update (PushMessage with default sender).
func (s *FakeTG) PushText(chatID int64, text string) int64 {
	return s.PushMessage(chatID, 0, text, FakeTGUser{})
}

// PushMessage queues an inbound text message and returns its monotonically
// increasing update_id.
func (s *FakeTG) PushMessage(chatID int64, thread int, text string, from FakeTGUser) int64 {
	return s.PushUpdate(FakeTGUpdate{Message: s.inboundMessage(chatID, thread, text, from)})
}

// PushCommand queues an inbound bot command with a bot_command entity.
func (s *FakeTG) PushCommand(chatID int64, thread int, command string, from FakeTGUser) int64 {
	msg := s.inboundMessage(chatID, thread, command, from)
	length := len(command)
	if i := strings.IndexAny(command, " \t"); i >= 0 {
		length = i
	}
	msg.Entities = []FakeTGMessageEntity{{Type: "bot_command", Offset: 0, Length: length}}
	return s.PushUpdate(FakeTGUpdate{Message: msg})
}

// PushEdit queues an edited_message update for an existing message id.
func (s *FakeTG) PushEdit(chatID int64, thread int, messageID int64, text string, from FakeTGUser) int64 {
	if from.ID == 0 {
		from = FakeTGUser{ID: chatID, FirstName: "user"}
	}
	now := time.Now().Unix()
	msg := &FakeTGMessage{
		MessageID:       messageID,
		MessageThreadID: thread,
		From:            &from,
		Date:            now,
		EditDate:        now,
		Chat:            FakeTGChat{ID: chatID, Type: "private"},
		Text:            text,
	}
	return s.PushUpdate(FakeTGUpdate{EditedMessage: msg})
}

// PushMedia queues an inbound photo/document message carrying file_id.
func (s *FakeTG) PushMedia(chatID int64, thread int, media FakeTGMedia, from FakeTGUser) int64 {
	msg := s.inboundMessage(chatID, thread, "", from)
	msg.Caption = media.Caption
	if media.Kind == "photo" {
		msg.Photo = []FakeTGPhotoSize{{
			FileID:       media.FileID,
			FileUniqueID: "unique-" + media.FileID,
			Width:        1,
			Height:       1,
		}}
	} else {
		mimeType := media.Kind
		if !strings.Contains(mimeType, "/") {
			mimeType = "application/octet-stream"
		}
		msg.Document = &FakeTGDocument{
			FileID:       media.FileID,
			FileUniqueID: "unique-" + media.FileID,
			FileName:     media.FileID,
			MimeType:     mimeType,
		}
	}
	return s.PushUpdate(FakeTGUpdate{Message: msg})
}

// PushCallback queues a callback-query update with a default sender.
func (s *FakeTG) PushCallback(chatID int64, data string) int64 {
	return s.PushCallbackFrom(chatID, 0, data, FakeTGUser{})
}

// PushCallbackFrom queues a callback-query update from an explicit user/thread.
func (s *FakeTG) PushCallbackFrom(chatID int64, thread int, data string, from FakeTGUser) int64 {
	if from.ID == 0 {
		from = FakeTGUser{ID: chatID, FirstName: "user"}
	}
	msg := s.inboundMessage(chatID, thread, "", FakeTGUser{})
	s.mu.Lock()
	cbID := fmt.Sprintf("cb-%d", msg.MessageID)
	s.callbackData[cbID] = data
	s.mu.Unlock()
	return s.PushUpdate(FakeTGUpdate{CallbackQuery: &FakeTGCallbackQuery{
		ID:      cbID,
		From:    from,
		Data:    data,
		Message: msg,
	}})
}

func (s *FakeTG) inboundMessage(chatID int64, thread int, text string, from FakeTGUser) *FakeTGMessage {
	if from.ID == 0 {
		from = FakeTGUser{ID: chatID, FirstName: "user"}
	}
	s.mu.Lock()
	s.nextMessageID++
	id := s.nextMessageID
	s.mu.Unlock()
	return &FakeTGMessage{
		MessageID:       id,
		MessageThreadID: thread,
		From:            &from,
		Date:            time.Now().Unix(),
		Chat:            FakeTGChat{ID: chatID, Type: "private"},
		Text:            text,
	}
}

// Active reports how many handlers are currently in flight.
func (s *FakeTG) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// WaitIdle waits until no handler is in flight, or returns the last error.
func (s *FakeTG) WaitIdle(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for s.Active() > 0 {
		if time.Now().After(deadline) {
			return fmt.Errorf("fake telegram: %d handler(s) still active after %s", s.Active(), timeout)
		}
		timer := time.NewTimer(2 * time.Millisecond)
		<-timer.C
	}
	return nil
}

// DecodeFakeTGError decodes an error envelope; ok responses are an error.
func DecodeFakeTGError(body []byte) (FakeTGAPIError, error) {
	var env tgEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return FakeTGAPIError{}, fmt.Errorf("fake telegram: decode error envelope: %w", err)
	}
	if env.OK {
		return FakeTGAPIError{}, errors.New("fake telegram: response is not an error")
	}
	out := FakeTGAPIError{Code: env.ErrorCode, Description: env.Description}
	if env.Parameters != nil {
		out.RetryAfter = env.Parameters.RetryAfter
	}
	return out, nil
}

type tgEnvelope struct {
	OK          bool          `json:"ok"`
	Result      any           `json:"result,omitempty"`
	ErrorCode   int           `json:"error_code,omitempty"`
	Description string        `json:"description,omitempty"`
	Parameters  *tgParameters `json:"parameters,omitempty"`
}

type tgParameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
}

func (s *FakeTG) begin() func() {
	s.mu.Lock()
	s.active++
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}
}

type tgStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *tgStatusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *tgStatusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	if err != nil {
		return n, fmt.Errorf("fake telegram: write response: %w", err)
	}
	return n, nil
}

func (w *tgStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *FakeTG) handle(w http.ResponseWriter, r *http.Request) {
	done := s.begin()
	defer done()
	sw := &tgStatusWriter{ResponseWriter: w}
	seq := 0
	defer func() {
		if seq > 0 {
			s.finishRequest(seq, sw.status)
		}
	}()
	if strings.HasPrefix(r.URL.Path, "/file/") {
		s.serveFile(sw, r)
		return
	}
	token, method, ok := splitBotPath(r.URL.Path)
	if !ok {
		writeTGError(sw, http.StatusNotFound, "Not Found", 0)
		return
	}
	if token != s.token {
		writeTGError(sw, http.StatusUnauthorized, "Unauthorized", 0)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeTGError(sw, http.StatusBadRequest, "Bad Request: cannot parse form", 0)
		return
	}
	req := s.record(r, method)
	seq = req.Seq
	if !s.waitDelay(r.Context(), s.delays[method]) {
		return
	}
	if fault, hit := s.takeFault(method); hit {
		if s.applyFault(sw, r, fault) {
			return
		}
	}
	s.dispatch(sw, r, method, req)
}

func (s *FakeTG) finishRequest(seq, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq > 0 && seq <= len(s.reqs) {
		s.reqs[seq-1].Status = status
	}
}

func (s *FakeTG) setResultMessageID(seq int, messageID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq > 0 && seq <= len(s.reqs) {
		s.reqs[seq-1].ResultMessageID = messageID
	}
}

func splitBotPath(path string) (token, method string, ok bool) {
	rest, found := strings.CutPrefix(path, "/bot")
	if !found {
		return "", "", false
	}
	token, method, found = strings.Cut(rest, "/")
	if !found || token == "" || method == "" || strings.Contains(method, "/") {
		return "", "", false
	}
	return token, method, true
}

func (s *FakeTG) record(r *http.Request, method string) *FakeTGRequest {
	params := make(map[string]string, len(r.Form))
	for key, values := range r.Form {
		if len(values) > 0 {
			params[key] = values[0]
		}
	}
	req := FakeTGRequest{
		Method:          method,
		ChatID:          formInt(r.Form, "chat_id"),
		MessageID:       formInt(r.Form, "message_id"),
		Text:            r.Form.Get("text"),
		CallbackQueryID: r.Form.Get("callback_query_id"),
		CallbackData:    r.Form.Get("callback_data"),
		Offset:          formInt(r.Form, "offset"),
		Timeout:         formSeconds(r.Form, "timeout", 50),
		Limit:           formSeconds(r.Form, "limit", 100),
		Params:          params,
	}
	s.mu.Lock()
	if req.CallbackData == "" {
		req.CallbackData = s.callbackData[req.CallbackQueryID]
	}
	s.seq++
	req.Seq = s.seq
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	return &req
}

func (s *FakeTG) waitDelay(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *FakeTG) takeFault(method string) (FakeTGFault, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fault, ok := s.faults[method]
	if !ok {
		return FakeTGFault{}, false
	}
	s.calls[method]++
	if fault.Every > 0 && s.calls[method]%fault.Every != 0 {
		return FakeTGFault{}, false
	}
	if fault.Times > 0 && s.faulted[method] >= fault.Times {
		return FakeTGFault{}, false
	}
	s.faulted[method]++
	return fault, true
}

func (s *FakeTG) applyFault(w http.ResponseWriter, r *http.Request, fault FakeTGFault) bool {
	switch {
	case fault.Close:
		if !abortConnection(w) {
			writeTGError(w, http.StatusInternalServerError, "Internal Server Error", 0)
		}
		return true
	case fault.Drop:
		<-r.Context().Done()
		return true
	case fault.Status >= http.StatusBadRequest:
		writeTGError(w, fault.Status, faultDescription(fault), fault.RetryAfter)
		return true
	default:
		return false
	}
}

func faultDescription(fault FakeTGFault) string {
	if fault.Status == http.StatusTooManyRequests {
		return fmt.Sprintf("Too Many Requests: retry after %d", fault.RetryAfter)
	}
	return http.StatusText(fault.Status)
}

func abortConnection(w http.ResponseWriter) bool {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return false
	}
	//nolint:errcheck // the connection is dropped by definition; its close result is irrelevant
	_ = conn.Close()
	return true
}

func (s *FakeTG) dispatch(w http.ResponseWriter, r *http.Request, method string, req *FakeTGRequest) {
	switch method {
	case "getUpdates":
		s.getUpdates(w, r, req)
	case "sendMessage":
		s.sendMessage(w, req)
	case "editMessageText":
		s.editMessageText(w, req)
	case "deleteMessage":
		s.deleteMessage(w, req)
	case "answerCallbackQuery":
		s.answerCallbackQuery(w, req)
	case "setMyCommands":
		writeTGResult(w, true)
	case "sendChatAction":
		writeTGResult(w, true)
	case "getMe":
		writeTGResult(w, s.bot)
	case "getFile":
		s.getFile(w, req)
	default:
		writeTGError(w, http.StatusNotFound, "Not Found", 0)
	}
}

func (s *FakeTG) getUpdates(w http.ResponseWriter, r *http.Request, req *FakeTGRequest) {
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	deadline := time.Now().Add(time.Duration(req.Timeout) * time.Second)
	for {
		updates, wait := s.pendingUpdates(req.Offset, limit)
		if len(updates) > 0 {
			writeTGResult(w, updates)
			return
		}
		remaining := time.Until(deadline)
		if req.Timeout == 0 || remaining <= 0 {
			writeTGResult(w, []FakeTGUpdate{})
			return
		}
		timer := time.NewTimer(remaining)
		select {
		case <-wait:
			timer.Stop()
		case <-timer.C:
			writeTGResult(w, []FakeTGUpdate{})
			return
		case <-r.Context().Done():
			timer.Stop()
			return
		}
	}
}

func (s *FakeTG) pendingUpdates(offset int64, limit int) ([]FakeTGUpdate, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.updates[:0]
	for _, update := range s.updates {
		if update.UpdateID >= offset {
			kept = append(kept, update)
		}
	}
	s.updates = kept
	batch := append([]FakeTGUpdate(nil), s.updates...)
	slices.SortFunc(batch, func(a, b FakeTGUpdate) int { return cmp.Compare(a.UpdateID, b.UpdateID) })
	if len(batch) > limit {
		batch = batch[:limit]
	}
	return batch, s.notify
}

func (s *FakeTG) sendMessage(w http.ResponseWriter, req *FakeTGRequest) {
	if req.ChatID == 0 {
		writeTGError(w, http.StatusBadRequest, "Bad Request: chat_id is empty", 0)
		return
	}
	if req.Text == "" {
		writeTGError(w, http.StatusBadRequest, "Bad Request: text is empty", 0)
		return
	}
	msg := s.newMessage(req.ChatID, req.Text)
	s.setResultMessageID(req.Seq, msg.MessageID)
	writeTGResult(w, msg)
}

func (s *FakeTG) newMessage(chatID int64, text string) FakeTGMessage {
	s.mu.Lock()
	s.nextMessageID++
	id := s.nextMessageID
	s.mu.Unlock()
	return FakeTGMessage{
		MessageID: id,
		From:      &s.bot,
		Date:      time.Now().Unix(),
		Chat:      FakeTGChat{ID: chatID, Type: "private"},
		Text:      text,
	}
}

func (s *FakeTG) editMessageText(w http.ResponseWriter, req *FakeTGRequest) {
	if req.MessageID == 0 {
		writeTGError(w, http.StatusBadRequest, "Bad Request: message_id is required", 0)
		return
	}
	s.setResultMessageID(req.Seq, req.MessageID)
	writeTGResult(w, FakeTGMessage{
		MessageID: req.MessageID,
		From:      &s.bot,
		Date:      time.Now().Unix(),
		Chat:      FakeTGChat{ID: req.ChatID, Type: "private"},
		Text:      req.Text,
	})
}

func (s *FakeTG) deleteMessage(w http.ResponseWriter, req *FakeTGRequest) {
	if req.ChatID == 0 || req.MessageID == 0 {
		writeTGError(w, http.StatusBadRequest, "Bad Request: chat_id and message_id are required", 0)
		return
	}
	writeTGResult(w, true)
}

func (s *FakeTG) answerCallbackQuery(w http.ResponseWriter, req *FakeTGRequest) {
	if req.CallbackQueryID == "" {
		writeTGError(w, http.StatusBadRequest, "Bad Request: callback_query_id is required", 0)
		return
	}
	writeTGResult(w, true)
}

func (s *FakeTG) getFile(w http.ResponseWriter, req *FakeTGRequest) {
	file, ok := s.files[req.Params["file_id"]]
	if !ok {
		writeTGError(w, http.StatusBadRequest, "Bad Request: invalid file_id", 0)
		return
	}
	writeTGResult(w, FakeTGFileInfo{
		FileID:       req.Params["file_id"],
		FileUniqueID: "unique-" + req.Params["file_id"],
		FileSize:     len(file.Data),
		FilePath:     file.Path,
	})
}

func (s *FakeTG) serveFile(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/file/bot")
	if !ok {
		writeTGError(w, http.StatusNotFound, "Not Found", 0)
		return
	}
	token, path, ok := strings.Cut(rest, "/")
	if !ok || token != s.token {
		writeTGError(w, http.StatusUnauthorized, "Unauthorized", 0)
		return
	}
	file, ok := s.filePaths[path]
	if !ok {
		writeTGError(w, http.StatusNotFound, "Not Found", 0)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(file.Data)))
	if _, err := w.Write(file.Data); err != nil {
		return
	}
}

func writeTGResult(w http.ResponseWriter, result any) {
	writeTGJSON(w, http.StatusOK, tgEnvelope{OK: true, Result: result})
}

func writeTGError(w http.ResponseWriter, status int, description string, retryAfter int) {
	env := tgEnvelope{ErrorCode: status, Description: description}
	if retryAfter > 0 {
		env.Parameters = &tgParameters{RetryAfter: retryAfter}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	writeTGJSON(w, status, env)
}

func writeTGJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(body, '\n')); err != nil {
		return
	}
}

func formInt(form url.Values, key string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(form.Get(key)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func formSeconds(form url.Values, key string, maxValue int) int {
	n, err := strconv.Atoi(strings.TrimSpace(form.Get(key)))
	if err != nil || n < 0 {
		return 0
	}
	if n > maxValue {
		return maxValue
	}
	return n
}
