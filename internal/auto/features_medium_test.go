//go:build !minimal

package auto

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
)

func TestF8AUTO001BackgroundReview(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: "prefer gofmt\n\nadd a test", Usage: budget.Usage{Input: 1, Output: 1}}}
	off := mustNew(t, Config{}, fake)
	if _, err := (&Reviewer{}).MaybeReview(context.Background(), off, 10); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if fake.count() != 0 {
		t.Fatal("disabled review called the provider")
	}
	on := mustNew(t, enabled(Review), fake)
	r := &Reviewer{}
	for range 9 {
		r.Turn()
	}
	if got, err := r.MaybeReview(context.Background(), on, 10); err != nil || got != nil {
		t.Fatalf("below turn threshold: %+v %v", got, err)
	}
	if fake.count() != 0 {
		t.Fatal("review ran below the threshold")
	}
	r.Turn()
	got, err := r.MaybeReview(context.Background(), on, 9)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if len(got.Suggestions) != 2 || got.Suggestions[0] != "prefer gofmt" {
		t.Fatalf("suggestions = %+v", got)
	}
	if fake.count() != 1 {
		t.Fatalf("calls = %d", fake.count())
	}
	// The next review must wait another 10 turns.
	if got, err := r.MaybeReview(context.Background(), on, 20); err != nil || got != nil {
		t.Fatalf("rate limit: %+v %v", got, err)
	}
	if fake.count() != 1 {
		t.Fatal("rate limit not enforced")
	}
}

func TestF8AUTO003Curator(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: "retire: old-skill", Usage: budget.Usage{Input: 1, Output: 1}}}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	in := CuratorInput{Now: now, IdleSince: now.Add(-3 * time.Hour), Skills: []string{"a", "b"}}
	off := mustNew(t, Config{}, fake)
	if _, err := (&CuratorState{}).MaybeCurate(context.Background(), off, in); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if fake.count() != 0 {
		t.Fatal("disabled curator called the provider")
	}
	on := mustNew(t, enabled(Curator), fake)
	c := &CuratorState{}
	res, err := c.MaybeCurate(context.Background(), on, in)
	if err != nil || len(res.Proposals) != 1 {
		t.Fatalf("curate: %+v %v", res, err)
	}
	if fake.count() != 1 {
		t.Fatalf("calls = %d", fake.count())
	}
	in.Now = now.Add(time.Hour)
	if res, err := c.MaybeCurate(context.Background(), on, in); err != nil || res != nil {
		t.Fatalf("daily limit: %+v %v", res, err)
	}
	if fake.count() != 1 {
		t.Fatal("curator ran twice in a day")
	}
	in.Now = now.Add(25 * time.Hour)
	in.IdleSince = in.Now.Add(-10 * time.Minute)
	if res, err := c.MaybeCurate(context.Background(), on, in); err != nil || res != nil {
		t.Fatalf("idle floor: %+v %v", res, err)
	}
	if fake.count() != 1 {
		t.Fatal("curator ran while not idle")
	}
}

func TestF8AUTO012Blueprints(t *testing.T) {
	t.Parallel()
	off := mustNew(t, Config{}, nil)
	if _, err := Instantiate(off, "daily-review", map[string]string{"time": "02:00"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	on := mustNew(t, enabled(Blueprints), nil)
	if got := BlueprintCatalog(); len(got) != 2 {
		t.Fatalf("catalog = %d, want 2", len(got))
	}
	bp, err := Instantiate(on, "daily-review", map[string]string{"time": "02:00"})
	if err != nil || bp.Name != "daily-review" {
		t.Fatalf("instantiate: %+v %v", bp, err)
	}
	if _, err := Instantiate(on, "daily-review", map[string]string{}); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing slot: %v", err)
	}
	if _, err := Instantiate(on, "nope", map[string]string{}); !errors.Is(err, ErrUnknownFeature) {
		t.Fatalf("unknown blueprint: %v", err)
	}
}

func TestF8AUTO014Goals(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: `{"achieved":true,"reason":"done"}`, Usage: budget.Usage{Input: 1, Output: 1}}}
	g, err := NewGoal("ship F8", 0)
	if err != nil || g.MaxTurns != goalsMaxTurns {
		t.Fatalf("NewGoal: %+v %v", g, err)
	}
	if _, emptyErr := NewGoal("", 5); !errors.Is(emptyErr, ErrConfig) {
		t.Fatalf("empty goal: %v", emptyErr)
	}
	if _, maxErr := NewGoal("x", 21); !errors.Is(maxErr, ErrConfig) {
		t.Fatalf("over max turns: %v", maxErr)
	}
	off := mustNew(t, Config{}, fake)
	if _, judgeErr := JudgeGoal(context.Background(), off, g); !errors.Is(judgeErr, ErrDisabled) {
		t.Fatalf("disabled: %v", judgeErr)
	}
	if fake.count() != 0 {
		t.Fatal("disabled judge called the provider")
	}
	on := mustNew(t, enabled(Goals), fake)
	v, err := JudgeGoal(context.Background(), on, g)
	if err != nil || !v.Achieved || v.Reason != "done" {
		t.Fatalf("verdict: %+v %v", v, err)
	}
	exhausted := g
	exhausted.Turns = g.MaxTurns
	if v, err := JudgeGoal(context.Background(), on, exhausted); err != nil || v != (GoalVerdict{}) {
		t.Fatalf("exhausted: %+v %v", v, err)
	}
	if fake.count() != 1 {
		t.Fatal("exhausted goal called the provider")
	}
}

func TestF8AUTO015Loops(t *testing.T) {
	t.Parallel()
	if _, err := NewLoop(LoopSpec{Interval: 10 * time.Second, Prompt: "x"}); !errors.Is(err, ErrConfig) {
		t.Fatalf("interval floor: %v", err)
	}
	spec, err := NewLoop(LoopSpec{Interval: 30 * time.Second, Prompt: "x", Times: 2})
	if err != nil {
		t.Fatalf("NewLoop: %v", err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	sch := NewScheduler(func() time.Time { return now })
	runs := 0
	run := func(context.Context) error { runs++; return nil }
	off := mustNew(t, Config{}, nil)
	if err := RegisterLoop(context.Background(), off, sch, spec, run); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if sch.Pending() != 0 {
		t.Fatal("disabled loop registered a task")
	}
	on := mustNew(t, enabled(Loops), nil)
	if err := RegisterLoop(context.Background(), on, sch, spec, run); err != nil {
		t.Fatalf("RegisterLoop: %v", err)
	}
	for range 3 {
		now = now.Add(30 * time.Second)
		sch.Tick(context.Background())
	}
	if runs != 2 {
		t.Fatalf("runs = %d, want 2 (times limit)", runs)
	}
}

func TestF8AUTO016Heartbeat(t *testing.T) {
	t.Parallel()
	if _, err := NewHeartbeat(10*time.Second, "x"); !errors.Is(err, ErrConfig) {
		t.Fatalf("interval floor: %v", err)
	}
	h, err := NewHeartbeat(60*time.Second, "check in")
	if err != nil {
		t.Fatalf("NewHeartbeat: %v", err)
	}
	if h.Interval() != 60*time.Second {
		t.Fatalf("interval = %s", h.Interval())
	}
	runs := 0
	run := func(_ context.Context, prompt string) error {
		runs++
		if prompt != "check in" {
			t.Errorf("prompt = %q", prompt)
		}
		return nil
	}
	off := mustNew(t, Config{}, nil)
	if _, err := h.Tick(context.Background(), off, true, run); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if runs != 0 {
		t.Fatal("disabled heartbeat ran")
	}
	on := mustNew(t, enabled(Heartbeat), nil)
	checkHeartbeatTick(t, h, on, false, run, false, 0)
	checkHeartbeatTick(t, h, on, true, run, true, 1)
	h.Pause()
	checkHeartbeatTick(t, h, on, true, run, false, 0)
	h.Resume()
	checkHeartbeatTick(t, h, on, true, run, true, 1)
}

func checkHeartbeatTick(t *testing.T, h *HeartbeatState, s *Service, idle bool, run func(context.Context, string) error, wantRan bool, wantRuns int) {
	t.Helper()
	runs := 0
	wrapped := func(ctx context.Context, prompt string) error {
		runs++
		return run(ctx, prompt)
	}
	ran, err := h.Tick(context.Background(), s, idle, wrapped)
	if err != nil || ran != wantRan || runs != wantRuns {
		t.Fatalf("tick idle=%v: ran=%v err=%v runs=%d, want ran=%v runs=%d", idle, ran, err, runs, wantRan, wantRuns)
	}
}

type fakeSender struct {
	calls   int
	target  string
	headers map[string]string
	body    []byte
	err     error
}

func (f *fakeSender) Send(_ context.Context, target string, headers map[string]string, body []byte) error {
	f.calls++
	f.target, f.headers, f.body = target, headers, body
	return f.err
}

func TestF8AUTO032OutboundWebhooks(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{}
	cfg := WebhookConfig{URL: "https://example.test/hook", Secret: "s3cret"}
	body := []byte(`{"event":"turn"}`)
	off := mustNew(t, Config{}, nil)
	if err := SendWebhook(context.Background(), off, sender, cfg, body); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if sender.calls != 0 {
		t.Fatal("disabled webhook sent")
	}
	on := mustNew(t, enabled(OutboundWebhook), nil)
	if err := SendWebhook(context.Background(), on, sender, cfg, body); err != nil {
		t.Fatalf("send: %v", err)
	}
	if sender.calls != 1 || sender.target != cfg.URL || !bytes.Equal(sender.body, body) {
		t.Fatalf("sender = %+v", sender)
	}
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	_, _ = mac.Write(body)
	if got := sender.headers[SignatureHeader]; got != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature = %q", got)
	}
	if err := SendWebhook(context.Background(), on, sender, WebhookConfig{URL: "http://example.test", Secret: "x"}, body); !errors.Is(err, ErrConfig) {
		t.Fatalf("public http: %v", err)
	}
	if err := SendWebhook(context.Background(), on, sender, WebhookConfig{URL: "http://127.0.0.1:9/hook", Secret: "x"}, body); err != nil {
		t.Fatalf("loopback http: %v", err)
	}
	if sender.calls != 2 {
		t.Fatalf("calls = %d, want 2", sender.calls)
	}
	var raw map[string]string
	if err := json.Unmarshal(body, &raw); err != nil || raw["event"] != "turn" {
		t.Fatalf("body fixture: %v", err)
	}
	if !strings.Contains(strings.ToLower(sender.headers["Content-Type"]), "json") {
		t.Fatalf("content type = %q", sender.headers["Content-Type"])
	}
}
