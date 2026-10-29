package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestFakeTelegramGetUpdatesOffset(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{})
	s.PushText(1001, "first")
	second := s.PushText(1001, "second")

	status, body := callTG(t, s, "getUpdates", url.Values{"offset": {"0"}})
	if status != http.StatusOK {
		t.Fatalf("getUpdates status = %d, want 200: %s", status, body)
	}
	updates := decodeTGResult[[]FakeTGUpdate](t, body)
	if len(updates) != 2 {
		t.Fatalf("updates = %d, want 2", len(updates))
	}
	if updates[0].Message == nil || updates[0].Message.Text != "first" || updates[1].Message == nil || updates[1].Message.Text != "second" {
		t.Fatalf("updates out of order: %+v", updates)
	}

	acked := strconv.FormatInt(second+1, 10)
	status, body = callTG(t, s, "getUpdates", url.Values{"offset": {acked}})
	if status != http.StatusOK {
		t.Fatalf("acked getUpdates status = %d: %s", status, body)
	}
	if got := decodeTGResult[[]FakeTGUpdate](t, body); len(got) != 0 {
		t.Fatalf("acked updates were delivered again: %+v", got)
	}

	status, body = callTG(t, s, "getUpdates", url.Values{"offset": {"0"}})
	if status != http.StatusOK {
		t.Fatalf("rewound getUpdates status = %d: %s", status, body)
	}
	if got := decodeTGResult[[]FakeTGUpdate](t, body); len(got) != 0 {
		t.Fatalf("lowering offset revived acked updates: %+v", got)
	}
}

func TestFakeTelegramGetUpdatesTimeout(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{})
	start := time.Now()
	status, body := callTG(t, s, "getUpdates", url.Values{"offset": {"1"}, "timeout": {"1"}})
	elapsed := time.Since(start)
	if status != http.StatusOK {
		t.Fatalf("getUpdates status = %d: %s", status, body)
	}
	if got := decodeTGResult[[]FakeTGUpdate](t, body); len(got) != 0 {
		t.Fatalf("timeout returned updates: %+v", got)
	}
	if elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("timeout=1 blocked for %s, want about 1s", elapsed)
	}
}

func TestFakeTelegramGetUpdatesWake(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type result struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		status, body, err := s.Call(ctx, "getUpdates", url.Values{"offset": {"0"}, "timeout": {"5"}})
		done <- result{status: status, body: body, err: err}
	}()
	timer := time.NewTimer(100 * time.Millisecond)
	<-timer.C
	id := s.PushText(1001, "wake")
	res := <-done
	if res.err != nil {
		t.Fatalf("long poll failed: %v", res.err)
	}
	updates := decodeTGResult[[]FakeTGUpdate](t, res.body)
	if len(updates) != 1 || updates[0].UpdateID != id || updates[0].Message == nil || updates[0].Message.Text != "wake" {
		t.Fatalf("woken poll = %+v (pushed id %d)", updates, id)
	}
}

func TestFakeTelegramSendAndEdit(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{})
	status, body := callTG(t, s, "sendMessage", url.Values{"chat_id": {"7"}, "text": {"hello"}})
	if status != http.StatusOK {
		t.Fatalf("sendMessage status = %d: %s", status, body)
	}
	sent := decodeTGResult[FakeTGMessage](t, body)
	if sent.MessageID == 0 || sent.Text != "hello" || sent.Chat.ID != 7 {
		t.Fatalf("sendMessage result = %+v", sent)
	}
	edit := url.Values{
		"chat_id":    {"7"},
		"message_id": {strconv.FormatInt(sent.MessageID, 10)},
		"text":       {"edited"},
	}
	status, body = callTG(t, s, "editMessageText", edit)
	if status != http.StatusOK {
		t.Fatalf("editMessageText status = %d: %s", status, body)
	}
	reqs := s.Requests()
	checkSendEditRecords(t, reqs, sent.MessageID)
	if got := s.RequestsFor("sendMessage"); len(got) != 1 || got[0].Text != "hello" {
		t.Fatalf("sendMessage records = %+v", got)
	}
}

func TestFakeTelegramEditRequiresMessageID(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{})
	status, body := callTG(t, s, "editMessageText", url.Values{"chat_id": {"7"}, "text": {"no id"}})
	if status != http.StatusBadRequest {
		t.Fatalf("edit without message_id status = %d, want 400: %s", status, body)
	}
	apiErr, err := DecodeFakeTGError(body)
	if err != nil {
		t.Fatalf("decode 400: %v", err)
	}
	if apiErr.Code != http.StatusBadRequest {
		t.Fatalf("error code = %d, want 400", apiErr.Code)
	}
}

func TestFakeTelegramCallbackUpdate(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{})
	id := s.PushCallback(1001, "approve:42")
	status, body := callTG(t, s, "getUpdates", url.Values{"offset": {"1"}})
	if status != http.StatusOK {
		t.Fatalf("callback getUpdates status = %d: %s", status, body)
	}
	updates := decodeTGResult[[]FakeTGUpdate](t, body)
	if len(updates) != 1 || updates[0].UpdateID != id || updates[0].CallbackQuery == nil {
		t.Fatalf("callback update = %+v (pushed id %d)", updates, id)
	}
	query := updates[0].CallbackQuery
	if query.Data != "approve:42" {
		t.Fatalf("callback data = %q", query.Data)
	}
	status, body = callTG(t, s, "answerCallbackQuery", url.Values{
		"callback_query_id": {query.ID},
		"text":              {"ok"},
	})
	if status != http.StatusOK {
		t.Fatalf("answerCallbackQuery status = %d: %s", status, body)
	}
	reqs := s.RequestsFor("answerCallbackQuery")
	if len(reqs) != 1 || reqs[0].CallbackQueryID != query.ID || reqs[0].CallbackData != "approve:42" {
		t.Fatalf("answer record = %+v", reqs)
	}
}

func checkSendEditRecords(t *testing.T, reqs []FakeTGRequest, messageID int64) {
	t.Helper()
	if len(reqs) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(reqs))
	}
	first, second := reqs[0], reqs[1]
	if first.Seq != 1 || first.Method != "sendMessage" || first.ChatID != 7 || first.Text != "hello" {
		t.Fatalf("first record = %+v", first)
	}
	if second.Seq != 2 || second.Method != "editMessageText" || second.ChatID != 7 ||
		second.MessageID != messageID || second.Text != "edited" {
		t.Fatalf("second record = %+v", second)
	}
}

func TestFakeTelegramRateLimit(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{Faults: map[string]FakeTGFault{
		"sendMessage": {Status: http.StatusTooManyRequests, RetryAfter: 3, Times: 1},
	}})
	form := url.Values{"chat_id": {"7"}, "text": {"limited"}}
	status, body := callTG(t, s, "sendMessage", form)
	if status != http.StatusTooManyRequests {
		t.Fatalf("first status = %d, want 429: %s", status, body)
	}
	apiErr, err := DecodeFakeTGError(body)
	if err != nil {
		t.Fatalf("decode 429: %v", err)
	}
	if apiErr.Code != http.StatusTooManyRequests || apiErr.RetryAfter != 3 {
		t.Fatalf("429 error = %+v, want code 429 retry_after 3", apiErr)
	}

	status, body = callTG(t, s, "sendMessage", form)
	if status != http.StatusOK {
		t.Fatalf("second status = %d, want 200: %s", status, body)
	}
	if sent := decodeTGResult[FakeTGMessage](t, body); sent.Text != "limited" {
		t.Fatalf("second result = %+v", sent)
	}
}

func TestFakeTelegramDrop(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{Faults: map[string]FakeTGFault{"getUpdates": {Drop: true}}})
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	status, _, err := s.Call(ctx, "getUpdates", url.Values{})
	elapsed := time.Since(start)
	if status != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drop returned status %d err %v, want context deadline", status, err)
	}
	if elapsed < 150*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("drop hung for %s, want about 200ms", elapsed)
	}
	if err := s.WaitIdle(time.Second); err != nil {
		t.Fatal(err)
	}
	if active := s.Active(); active != 0 {
		t.Fatalf("active handlers = %d after cancel, want 0", active)
	}
}

func TestFakeTelegramErrors(t *testing.T) {
	t.Parallel()
	s := NewFakeTG(t, FakeTGOptions{})
	status, body, err := s.Get(t.Context(), s.URL+"/botwrong-token/getMe")
	if err != nil {
		t.Fatalf("bad-token request: %v", err)
	}
	if status != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401: %s", status, body)
	}
	apiErr, err := DecodeFakeTGError(body)
	if err != nil {
		t.Fatalf("decode 401: %v", err)
	}
	if apiErr.Code != http.StatusUnauthorized || apiErr.Description == "" {
		t.Fatalf("401 body = %+v", apiErr)
	}

	status, body = callTG(t, s, "doesNotExist", url.Values{})
	if status != http.StatusNotFound {
		t.Fatalf("unknown method status = %d, want 404: %s", status, body)
	}
	apiErr, err = DecodeFakeTGError(body)
	if err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if apiErr.Code != http.StatusNotFound {
		t.Fatalf("404 body = %+v", apiErr)
	}
}

func TestFakeTelegramMedia(t *testing.T) {
	t.Parallel()
	want := []byte("small media payload")
	s := NewFakeTG(t, FakeTGOptions{Files: map[string]FakeTGFile{
		"file-1": {Path: "documents/note.txt", Data: want},
	}})
	status, body := callTG(t, s, "getFile", url.Values{"file_id": {"file-1"}})
	if status != http.StatusOK {
		t.Fatalf("getFile status = %d: %s", status, body)
	}
	info := decodeTGResult[FakeTGFileInfo](t, body)
	if info.FileID != "file-1" || info.FilePath != "documents/note.txt" || info.FileSize != len(want) {
		t.Fatalf("getFile result = %+v", info)
	}
	status, got, err := s.Get(t.Context(), s.FileURL(info.FilePath))
	if err != nil {
		t.Fatalf("file download: %v", err)
	}
	if status != http.StatusOK || !bytes.Equal(got, want) {
		t.Fatalf("download status = %d body = %q", status, got)
	}
}

func TestFakeTelegramFaults(t *testing.T) {
	t.Parallel()
	t.Run("5xx every second call", func(t *testing.T) {
		t.Parallel()
		s := NewFakeTG(t, FakeTGOptions{Faults: map[string]FakeTGFault{
			"sendMessage": {Status: http.StatusInternalServerError, Every: 2, Times: 2},
		}})
		want := []int{200, 500, 200, 500}
		for i, code := range want {
			status, body := callTG(t, s, "sendMessage", url.Values{"chat_id": {"7"}, "text": {"fault"}})
			if status != code {
				t.Fatalf("call %d status = %d, want %d: %s", i+1, status, code, body)
			}
		}
	})
	t.Run("connection close", func(t *testing.T) {
		t.Parallel()
		s := NewFakeTG(t, FakeTGOptions{Faults: map[string]FakeTGFault{
			"deleteMessage": {Close: true, Times: 1},
		}})
		form := url.Values{"chat_id": {"7"}, "message_id": {"1"}}
		status, _, err := s.Call(t.Context(), "deleteMessage", form)
		if err == nil {
			t.Fatalf("closed call returned status %d, want transport error", status)
		}
		status, body := callTG(t, s, "deleteMessage", form)
		if status != http.StatusOK {
			t.Fatalf("call after close status = %d: %s", status, body)
		}
	})
	t.Run("per-method delay", func(t *testing.T) {
		t.Parallel()
		s := NewFakeTG(t, FakeTGOptions{Delays: map[string]time.Duration{
			"editMessageText": 50 * time.Millisecond,
		}})
		start := time.Now()
		status, body := callTG(t, s, "editMessageText", url.Values{
			"chat_id": {"7"}, "message_id": {"1"}, "text": {"slow"},
		})
		elapsed := time.Since(start)
		if status != http.StatusOK {
			t.Fatalf("delayed edit status = %d: %s", status, body)
		}
		if elapsed < 40*time.Millisecond {
			t.Fatalf("delay = %s, want at least 40ms", elapsed)
		}
	})
}

func decodeTGResult[T any](t *testing.T, body []byte) T {
	t.Helper()
	var env struct {
		OK     bool `json:"ok"`
		Result T    `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode result %s: %v", body, err)
	}
	if !env.OK {
		t.Fatalf("result not ok: %s", body)
	}
	return env.Result
}

func callTG(t *testing.T, s *FakeTG, method string, form url.Values) (int, []byte) {
	t.Helper()
	status, body, err := s.Call(t.Context(), method, form)
	if err != nil {
		t.Fatalf("call %s: %v", method, err)
	}
	return status, body
}
