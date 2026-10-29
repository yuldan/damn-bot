package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTG is a loopback Bot API for adapter tests: it records calls in arrival
// order, queues updates, supports long polling and per-method fault injection.
type fakeTG struct {
	*httptest.Server
	token string

	mu         sync.Mutex
	calls      []fakeCall
	updates    []fakeUpdate
	nextID     int64
	nextMsg    int64
	confirmed  int64
	notify     chan struct{}
	faults     map[string][]fakeFault
	nth        map[string]map[int]fakeFault
	seenMethod map[string]int
	// pollWait caps the server-side long poll in tests that need a stall
	// threshold below the production 50 s window.
	pollWait time.Duration
	// cancelled counts hanging requests the client abandoned by cancelling.
	cancelled atomic.Int64
	// hold parks the next getUpdates until its request context is cancelled;
	// holdStart closes once the handler is waiting.
	hold      bool
	holdStart chan struct{}
}

type fakeCall struct {
	Seq    int
	Method string
	Form   url.Values
	Files  map[string][]byte
	Names  map[string]string
}

type fakeUpdate struct {
	ID  int64
	Raw json.RawMessage
}

// fakeFault is one injected failure; Times=0 means every matching call.
type fakeFault struct {
	Status     int
	RetryAfter int
	Drop       bool
	// Hang delays the response; if the client gives up first the handler
	// returns without a body, exactly like a wedged connection.
	Hang  time.Duration
	Times int
}

func newFakeTG(t *testing.T) *fakeTG {
	t.Helper()
	f := &fakeTG{
		token:      "424242:fake-telegram-token",
		notify:     make(chan struct{}),
		faults:     map[string][]fakeFault{},
		nth:        map[string]map[int]fakeFault{},
		seenMethod: map[string]int{},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTG) handle(w http.ResponseWriter, r *http.Request) {
	token, method, ok := splitPath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{ErrorCode: 404, Description: "Not Found"})
		return
	}
	if token != f.token {
		writeJSON(w, http.StatusUnauthorized, envelope{ErrorCode: 401, Description: "Unauthorized"})
		return
	}
	form, files, names, err := parseFakeBody(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{ErrorCode: 400, Description: "Bad Request"})
		return
	}
	if fault, hit := f.record(method, form, files, names); hit {
		if f.applyFault(w, r, fault) {
			return
		}
	}
	switch method {
	case "getUpdates":
		f.getUpdates(w, r)
	case "sendMessage":
		f.sendMessage(w, r.Form)
	case "sendPhoto", "sendDocument":
		field := "photo"
		if method == "sendDocument" {
			field = "document"
		}
		if len(files[field]) == 0 || form.Get("chat_id") == "" {
			writeJSON(w, http.StatusBadRequest, envelope{ErrorCode: 400, Description: "Bad Request: chat_id and file are required"})
			return
		}
		f.mu.Lock()
		f.nextMsg++
		id := f.nextMsg
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, envelope{OK: true, Result: mustJSON(tgMessage{
			MessageID: id, Chat: tgID{ID: formInt(form, "chat_id")},
		})})
	case "editMessageText":
		chat := formInt(r.Form, "chat_id")
		writeJSON(w, http.StatusOK, envelope{OK: true, Result: mustJSON(tgMessage{
			MessageID: formInt(r.Form, "message_id"), Chat: tgID{ID: chat}, Text: r.Form.Get("text"),
		})})
	case "answerCallbackQuery":
		writeJSON(w, http.StatusOK, envelope{OK: true, Result: mustJSON(true)})
	case "sendChatAction":
		writeJSON(w, http.StatusOK, envelope{OK: true, Result: mustJSON(true)})
	default:
		writeJSON(w, http.StatusNotFound, envelope{ErrorCode: 404, Description: "Not Found"})
	}
}

func splitPath(path string) (token, method string, ok bool) {
	rest, found := strings.CutPrefix(path, "/bot")
	if !found {
		return "", "", false
	}
	token, method, found = strings.Cut(rest, "/")
	return token, method, found && token != "" && method != ""
}

// parseFakeBody decodes both the urlencoded text calls and the multipart
// uploads, keeping the file bytes for assertions.
func parseFakeBody(w http.ResponseWriter, r *http.Request) (url.Values, map[string][]byte, map[string]string, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseForm(); err != nil {
			return nil, nil, nil, fmt.Errorf("fake: parse form: %w", err)
		}
		return r.Form, nil, nil, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fake: multipart reader: %w", err)
	}
	form, files, names := url.Values{}, map[string][]byte{}, map[string]string{}
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, nil, nil, fmt.Errorf("fake: next part: %w", nextErr)
		}
		data, readErr := io.ReadAll(part)
		closeErr := part.Close()
		if readErr != nil {
			return nil, nil, nil, fmt.Errorf("fake: read part: %w", readErr)
		}
		if closeErr != nil {
			return nil, nil, nil, fmt.Errorf("fake: close part: %w", closeErr)
		}
		if filename := part.FileName(); filename != "" {
			files[part.FormName()], names[part.FormName()] = data, filename
			continue
		}
		form.Add(part.FormName(), string(data))
	}
	return form, files, names, nil
}

// record appends the call and reports the next injected fault.
func (f *fakeTG) record(method string, form url.Values, files map[string][]byte, names map[string]string) (fakeFault, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	clone := url.Values{}
	for k, v := range form {
		clone[k] = append([]string(nil), v...)
	}
	cloneFiles, cloneNames := map[string][]byte{}, map[string]string{}
	for k, v := range files {
		cloneFiles[k] = append([]byte(nil), v...)
	}
	maps.Copy(cloneNames, names)
	f.calls = append(f.calls, fakeCall{Seq: len(f.calls) + 1, Method: method, Form: clone, Files: cloneFiles, Names: cloneNames})
	f.seenMethod[method]++
	if byCall, ok := f.nth[method]; ok {
		if fault, hit := byCall[f.seenMethod[method]]; hit {
			delete(byCall, f.seenMethod[method])
			return fault, true
		}
	}
	queue := f.faults[method]
	if len(queue) == 0 {
		return fakeFault{}, false
	}
	fault := queue[0]
	if fault.Times > 0 {
		fault.Times--
		if fault.Times == 0 {
			f.faults[method] = queue[1:]
		} else {
			f.faults[method][0] = fault
		}
	}
	return fault, true
}

// setFault replaces the injected queue of one method under the client lock.
// A running adapter reads this map from the HTTP handler goroutine, so a test
// must never assign it directly.
func (f *fakeTG) setFault(method string, faults ...fakeFault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults[method] = faults
}

// faultAt arms fault for the n-th call of method (1-based), leaving other
// calls untouched; the queued faults keep their own order.
func (f *fakeTG) faultAt(method string, n int, fault fakeFault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nth[method] == nil {
		f.nth[method] = map[int]fakeFault{}
	}
	f.nth[method][n] = fault
}

func (f *fakeTG) applyFault(w http.ResponseWriter, r *http.Request, fault fakeFault) bool {
	if fault.Hang > 0 {
		timer := time.NewTimer(fault.Hang)
		defer timer.Stop()
		select {
		case <-timer.C:
			return false // the delayed normal response follows
		case <-r.Context().Done():
			f.cancelled.Add(1)
			return true // the client gave up: never answer
		}
	}
	if fault.Drop {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, envelope{ErrorCode: 500, Description: "Internal Server Error"})
			return true
		}
		_ = conn.Close() //nolint:errcheck // the connection is dropped by definition
		return true
	}
	if fault.Status >= 400 {
		writeJSON(w, fault.Status, envelope{ErrorCode: fault.Status, Description: http.StatusText(fault.Status), Parameters: map[string]int{"retry_after": fault.RetryAfter}})
		return true
	}
	return false
}

// stallNextUpdate parks the next getUpdates until its request context is
// cancelled, like a wedged connection: no wall-clock duration is involved. The
// returned channel closes when the handler is actually parked, so tests
// synchronize on state, never a sleep.
func (f *fakeTG) stallNextUpdate() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hold, f.holdStart = true, make(chan struct{})
	return f.holdStart
}

// parkUpdate consumes a pending stallNextUpdate and waits until the client
// cancels the request. It reports whether the request was parked.
func (f *fakeTG) parkUpdate(r *http.Request) bool {
	f.mu.Lock()
	hold, start := f.hold, f.holdStart
	f.hold = false
	f.mu.Unlock()
	if !hold {
		return false
	}
	close(start)
	<-r.Context().Done()
	f.cancelled.Add(1)
	return true
}

// pollWindow caps the server-side long poll in tests that need a stall
// threshold below the production window.
func (f *fakeTG) pollWindow(timeout int64) time.Duration {
	f.mu.Lock()
	pollWait := f.pollWait
	f.mu.Unlock()
	wait := time.Duration(timeout) * time.Second
	if pollWait > 0 && timeout > 0 && wait > pollWait {
		wait = pollWait
	}
	return wait
}

func (f *fakeTG) getUpdates(w http.ResponseWriter, r *http.Request) {
	if f.parkUpdate(r) {
		return
	}
	offset := formInt(r.Form, "offset")
	timeout := formInt(r.Form, "timeout")
	limit := formInt(r.Form, "limit")
	if limit <= 0 {
		limit = 100
	}
	deadline := time.NewTimer(f.pollWindow(timeout))
	defer deadline.Stop()
	for {
		f.mu.Lock()
		if offset > f.confirmed {
			f.confirmed = offset
		}
		batch := make([]json.RawMessage, 0, limit)
		for _, u := range f.updates {
			if u.ID >= offset && len(batch) < int(limit) {
				batch = append(batch, u.Raw)
			}
		}
		wait := f.notify
		f.mu.Unlock()
		if len(batch) > 0 {
			writeJSON(w, http.StatusOK, envelope{OK: true, Result: mustJSON(batch)})
			return
		}
		if timeout == 0 {
			writeJSON(w, http.StatusOK, envelope{OK: true, Result: mustJSON(batch)})
			return
		}
		select {
		case <-wait:
		case <-deadline.C:
			writeJSON(w, http.StatusOK, envelope{OK: true, Result: mustJSON(batch)})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (f *fakeTG) sendMessage(w http.ResponseWriter, form url.Values) {
	chat := formInt(form, "chat_id")
	if form.Get("text") == "" || chat == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{ErrorCode: 400, Description: "Bad Request: chat_id and text are required"})
		return
	}
	f.mu.Lock()
	f.nextMsg++
	id := f.nextMsg
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, envelope{OK: true, Result: mustJSON(tgMessage{
		MessageID: id, Chat: tgID{ID: chat}, Text: form.Get("text"), MessageThreadID: formInt(form, "message_thread_id"),
	})})
}

// pushUpdate queues one update and wakes blocked polls.
func (f *fakeTG) pushUpdate(t *testing.T, body map[string]any) {
	t.Helper()
	f.mu.Lock()
	f.nextID++
	id := f.nextID
	body["update_id"] = id
	raw, err := json.Marshal(body)
	f.mu.Unlock()
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	f.mu.Lock()
	f.updates = append(f.updates, fakeUpdate{ID: id, Raw: raw})
	close(f.notify)
	f.notify = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakeTG) pushText(t *testing.T, chat, thread int64, text string) {
	t.Helper()
	msg := map[string]any{
		"message_id": chat*1000 + int64(len(text)),
		"from":       map[string]any{"id": chat},
		"chat":       map[string]any{"id": chat},
		"text":       text,
	}
	if thread != 0 {
		msg["message_thread_id"] = thread
	}
	f.pushMessage(t, msg)
}

// pushMessage queues one raw message object, for tests that need chat type,
// entities, or a reply target.
func (f *fakeTG) pushMessage(t *testing.T, msg map[string]any) {
	t.Helper()
	f.pushUpdate(t, map[string]any{"message": msg})
}

func (f *fakeTG) pushCallback(t *testing.T, chat int64, callbackID, data string) {
	t.Helper()
	f.pushUpdate(t, map[string]any{"callback_query": map[string]any{
		"id":      callbackID,
		"from":    map[string]any{"id": chat},
		"data":    data,
		"message": map[string]any{"message_id": 900, "chat": map[string]any{"id": chat}},
	}})
}

func (f *fakeTG) callsFor(method string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTG) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func writeJSON(w http.ResponseWriter, status int, env envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(env); err != nil {
		return
	}
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return raw
}

func formInt(form url.Values, key string) int64 {
	n, _ := strconv.ParseInt(form.Get(key), 10, 64) //nolint:errcheck // absent/invalid form values are zero
	return n
}

// memStore is the in-memory Store; keys are isolated per test.
type memStore struct {
	mu      sync.Mutex
	offset  int64
	ids     map[string]int64
	claimed map[string]bool
}

func newMemStore() *memStore {
	return &memStore{ids: map[string]int64{}, claimed: map[string]bool{}}
}

func (s *memStore) Offset(context.Context, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offset, nil
}

func (s *memStore) PutOffset(_ context.Context, _ string, next int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offset = next
	return nil
}

func (s *memStore) MessageID(_ context.Context, key string) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.ids[key]
	return id, ok, nil
}

func (s *memStore) PutMessageID(_ context.Context, key string, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[key] = id
	return nil
}

func (s *memStore) ClaimCallback(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[id] {
		return false, nil
	}
	s.claimed[id] = true
	return true, nil
}

// fakeThrottle records the 429 deadlines the adapter forwards to the shared
// limiter (EC-PLAT-014).
type fakeThrottle struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newFakeThrottle() *fakeThrottle {
	return &fakeThrottle{until: map[string]time.Time{}}
}

func (t *fakeThrottle) Penalize(chat string, until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.until[chat] = until
}

func (t *fakeThrottle) deadline(chat string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	until, ok := t.until[chat]
	return until, ok
}

// fakeClock records every wait and advances instantly.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake clock: %w", err)
	}
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

func (c *fakeClock) durations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// waitFor polls cond until true or the deadline expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// newTestAdapter builds an adapter around the fake with a fast clock.
func newTestAdapter(t *testing.T, f *fakeTG, store Store, opts ...func(*Config)) (*Adapter, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	cfg := &Config{
		Token:   f.token,
		BaseURL: f.URL,
		Doer:    f, // the fake implements Doer directly: no netx in unit tests
		Store:   store,
		Clock:   clock,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	return a, clock
}

// Do makes the fake server usable as a Doer for the adapter under test.
func (f *fakeTG) Do(req *http.Request) (*http.Response, error) {
	resp, err := f.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fake telegram: %w", err)
	}
	return resp, nil
}

// errorDoer fails the first `remaining` calls, then delegates to inner.
type errorDoer struct {
	inner     Doer
	err       error
	remaining int
}

func (d *errorDoer) Do(req *http.Request) (*http.Response, error) {
	if d.remaining > 0 {
		d.remaining--
		return nil, d.err
	}
	resp, err := d.inner.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error doer: %w", err)
	}
	return resp, nil
}

// errWithURL mimics a raw transport error that embeds the token-bearing URL.
func errWithURL(token string) error {
	return fmt.Errorf("Post \"https://api.telegram.org/bot%s/getUpdates\": dial tcp: timeout", token)
}
