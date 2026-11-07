package gwapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestF7PLAT028Responses(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{}
	h := newTestServer(t, eng, nil, testConfig()).Handler()
	rec := postPath(t, h, "/v1/responses", `{"model":"damnbot","input":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var obj ResponseObject
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if obj.Object != "response" || obj.Status != "completed" || obj.Model != "damnbot" {
		t.Errorf("object = %+v", obj)
	}
	if len(obj.Output) != 1 || obj.Output[0].Content[0].Text != "hello" || obj.Output[0].Content[0].Type != "output_text" {
		t.Errorf("output = %+v", obj.Output)
	}
	if obj.Usage == nil || obj.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v", obj.Usage)
	}
	getRec := get(t, h, "/v1/responses/"+obj.ID, authHeaders())
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `"id":"`+obj.ID+`"`) {
		t.Errorf("get = %d %s", getRec.Code, getRec.Body.String())
	}
	unknown := get(t, h, "/v1/responses/resp_missing", authHeaders())
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", unknown.Code)
	}
	if er := decodeError(t, unknown); er.Error.Code != "response_not_found" {
		t.Errorf("error = %+v", er.Error)
	}
}

func TestAPIServerResponsesStream(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{}
	h := newTestServer(t, eng, nil, testConfig()).Handler()
	rec := postPath(t, h, "/v1/responses", `{"model":"damnbot","input":"hi","stream":true}`)
	body := rec.Body.String()
	for _, want := range []string{"event: response.created", "event: response.output_text.delta", `"delta":"hello"`, "event: response.completed"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	if strings.Contains(body, "[DONE]") {
		t.Error("Responses SSE must not use [DONE]")
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestAPIServerResponseCancelIdempotent(t *testing.T) {
	t.Parallel()
	cancelled := make(chan struct{})
	started := make(chan struct{})
	eng := &fakeEngine{run: func(ctx context.Context, _ *Turn) (<-chan Update, error) {
		ch := make(chan Update, 1)
		ch <- Update{Kind: UpdateText, Text: "partial"}
		close(started)
		go func() {
			<-ctx.Done()
			close(cancelled)
			close(ch)
		}()
		return ch, nil
	}}
	h := newTestServer(t, eng, nil, testConfig()).Handler()
	rec := httptest.NewRecorder()
	req := request(t, http.MethodPost, "/v1/responses", `{"model":"damnbot","input":"hi","stream":true}`, authHeaders())
	handlerDone := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(handlerDone) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("engine never started")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		g := get(t, h, "/v1/responses/resp_test", authHeaders())
		if strings.Contains(g.Body.String(), `"status":"in_progress"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("response never became in_progress: %d %s", g.Code, g.Body.String())
		}
		<-time.After(time.Millisecond)
	}
	for range 2 {
		c := postPath(t, h, "/v1/responses/resp_test/cancel", `{}`)
		if c.Code != http.StatusOK || !strings.Contains(c.Body.String(), `"status":"cancelled"`) {
			t.Fatalf("cancel = %d %s", c.Code, c.Body.String())
		}
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("engine not cancelled")
	}
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return")
	}
	unknown := postPath(t, h, "/v1/responses/resp_missing/cancel", `{}`)
	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown cancel = %d", unknown.Code)
	}
}

func TestF7PLAT028Runs(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	eng := &fakeEngine{run: func(ctx context.Context, _ *Turn) (<-chan Update, error) {
		ch := scripted(Update{Kind: UpdateText, Text: "hello"}, stopUpdate("stop"))
		go func() {
			for range ch {
			}
			close(done)
		}()
		return ch, nil
	}}
	srv := newTestServer(t, eng, nil, testConfig())
	h := srv.Handler()
	rec := postPath(t, h, "/v1/runs", `{"model":"damnbot","input":"hi"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created RunObject
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Status != "queued" || created.Object != "run" || created.ID == "" {
		t.Fatalf("created = %+v", created)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run never finished")
	}
	waitRunDone(t, srv, created.ID)
	getRec := get(t, h, "/v1/runs/"+created.ID, authHeaders())
	var final RunObject
	if err := json.Unmarshal(getRec.Body.Bytes(), &final); err != nil {
		t.Fatalf("decode final: %v (%s)", err, getRec.Body.String())
	}
	if final.Status != "completed" || final.Output[0].Content[0].Text != "hello" || final.Usage.TotalTokens != 5 {
		t.Errorf("final = %+v", final)
	}
	unknown := get(t, h, "/v1/runs/run_missing", authHeaders())
	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown = %d", unknown.Code)
	}
}

func TestAPIServerRunsEventsAndCancel(t *testing.T) {
	t.Parallel()
	cancelled := make(chan struct{})
	started := make(chan struct{})
	eng := &fakeEngine{run: func(ctx context.Context, _ *Turn) (<-chan Update, error) {
		ch := make(chan Update, 1)
		close(started)
		go func() { <-ctx.Done(); close(cancelled); close(ch) }()
		return ch, nil
	}}
	srv := newTestServer(t, eng, nil, testConfig())
	h := srv.Handler()
	rec := postPath(t, h, "/v1/runs", `{"model":"damnbot","input":"hi"}`)
	var created RunObject
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("run never started")
	}
	for range 2 {
		c := postPath(t, h, "/v1/runs/"+created.ID+"/cancel", `{}`)
		if c.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", c.Code, c.Body.String())
		}
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("run not cancelled")
	}
	waitRunDone(t, srv, created.ID)
	ev := get(t, h, "/v1/runs/"+created.ID+"/events", authHeaders())
	if ev.Code != http.StatusOK || !strings.Contains(ev.Body.String(), "event: run.in_progress") || !strings.Contains(ev.Body.String(), "event: done") {
		t.Errorf("events = %d %s", ev.Code, ev.Body.String())
	}
}

func TestAPIServerResourcesLimits(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.BodyLimit = 128
	h := newTestServer(t, &fakeEngine{}, nil, cfg).Handler()
	big := `{"model":"damnbot","input":"` + strings.Repeat("x", 400) + `"}`
	for _, path := range []string{"/v1/responses", "/v1/runs"} {
		rec := postPath(t, h, path, big)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s status = %d, want 413", path, rec.Code)
		}
	}
	unknown := postPath(t, h, "/v1/runs/run_x/cancel", `{}`)
	if unknown.Code != http.StatusNotFound {
		t.Errorf("cancel unknown = %d", unknown.Code)
	}
}

func postPath(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request(t, http.MethodPost, path, body, authHeaders()))
	return rec
}

// waitRunDone blocks until executeRun has published its terminal state, so a
// finishing goroutine never outlives the test.
func waitRunDone(t *testing.T, srv *Server, id string) {
	t.Helper()
	srv.store.mu.Lock()
	rs := srv.store.runs[id]
	srv.store.mu.Unlock()
	if rs == nil {
		t.Fatalf("run %q is not registered", id)
	}
	select {
	case <-rs.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("run %q did not finish", id)
	}
}
