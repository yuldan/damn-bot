package provider_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// instant is the injected retry wait: tests over real loopback sockets
// cannot use a synctest bubble, so backoff time is recorded, not waited.
func instant(context.Context, time.Duration) error { return nil }

func route(model, url string) provider.Route {
	return provider.Route{Provider: "fake", Model: model, BaseURL: url, APIKey: "sk-test", ContextWindow: 128_000}
}

// client builds an OpenAI client over a loopback-only netx network.
func client(t *testing.T, cfg provider.Config) *provider.OpenAI {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	if cfg.Wait == nil {
		cfg.Wait = instant
	}
	o, err := provider.NewOpenAI(n.Client("provider", netx.Inference), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func ask(model string) *provider.Request {
	return &provider.Request{Model: model, Messages: []provider.Message{{Role: "user", Content: "hi"}}}
}

func TestDoStream(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("Hel", "lo"))
	o := client(t, provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
	var chunks []string
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), func(c provider.Chunk) { chunks = append(chunks, string(c.Content)) })
	if err != nil || resp.Content != "Hello" || resp.Attempts != 1 || resp.Usage.Input != 100 || resp.EstInputTokens <= 0 ||
		resp.Model != "m" || strings.Join(chunks, "|") != "Hel|lo" {
		t.Fatalf("%+v %v %v", resp, err, chunks)
	}
	if body := string(srv.Requests()[0]); !strings.Contains(body, `"model":"m"`) || !strings.Contains(body, `"stream":true`) {
		t.Errorf("request body %s", body)
	}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/nope"), nil); !errors.Is(err, provider.ErrNoRoute) {
		t.Errorf("unknown model: %v", err)
	}
	if _, err := provider.NewOpenAI(nil, provider.Config{Fallback: []string{"a", "b", "c"}}); !errors.Is(err, provider.ErrConfig) {
		t.Errorf("3 fallbacks: %v", err)
	}
}

// TestRetryAndClasses (P6): retryable statuses retry on the same model up
// to 3 attempts; non-retryable ones fail at once with their class.
func TestRetryAndClasses(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Status(503, "", ""), providertest.Status(429, "", ""), providertest.Text("ok"),
		providertest.Status(401, "invalid_api_key", ""),
		providertest.Status(400, "context_length_exceeded", ""))
	var retries []retry.Attempt
	o := client(t, provider.Config{Routes: []provider.Route{route("m", srv.URL)}, OnRetry: func(a retry.Attempt) { retries = append(retries, a) }})
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err != nil || resp.Attempts != 3 || len(retries) != 2 || retries[0].Class != retry.Overloaded || retries[1].Class != retry.RateLimited {
		t.Fatalf("%+v %v %+v", resp, err, retries)
	}
	for _, want := range []retry.Class{retry.Auth, retry.ContextLength} {
		_, err = o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != want || retry.ClassifyErr(err) != want {
			t.Errorf("want %s: %v", want, err)
		}
	}
	if n := len(srv.Requests()); n != 5 {
		t.Errorf("%d requests, want 5", n)
	}
}

// TestHostileStream: untrusted stream content maps to typed classes —
// malformed JSON and a truncated stream are stream_broken (retried), an
// oversized SSE event and an in-stream error do not crash or leak through,
// content_filter is returned as a finish reason, and tool-call fields are
// kept raw (names are not interpreted here).
func TestHostileStream(t *testing.T) {
	t.Parallel()
	bad := providertest.Reply{Frames: []string{`{"choices":[{"delta":{"content":"a"`}}
	cut := providertest.Reply{Frames: []string{`{"choices":[{"delta":{"content":"partial"}}]}`}}
	huge := providertest.Reply{Raw: "data: " + strings.Repeat("A", 5<<20) + "\n\n"}
	inStreamErr := providertest.Reply{Frames: []string{`{"error":{"message":"boom","code":"server_error"}}`}}
	filtered := providertest.Reply{Frames: []string{`{"choices":[{"delta":{"content":"par","tool_calls":[{"index":0,"id":"c","function":{"name":"../../rm -rf","arguments":"{\"x\":1}"}}]},"finish_reason":"content_filter"}]}`, "[DONE]"}}
	cases := []struct {
		script []providertest.Reply
		want   retry.Class
	}{
		{[]providertest.Reply{bad, bad, bad}, retry.StreamBroken},
		{[]providertest.Reply{cut, cut, cut}, retry.StreamBroken},
		{[]providertest.Reply{huge}, retry.Unclassified},
		{[]providertest.Reply{inStreamErr, inStreamErr, inStreamErr}, retry.Server},
	}
	for _, c := range cases {
		srv := providertest.New(t, nil, c.script...)
		o := client(t, provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
		if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); retry.ClassifyErr(err) != c.want {
			t.Errorf("want %s: %v", c.want, err)
		}
	}
	srv := providertest.New(t, nil, filtered)
	o := client(t, provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err != nil || resp.FinishReason != provider.FinishContentFilter || resp.Content != "par" ||
		len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "../../rm -rf" || resp.ToolCalls[0].ArgumentsRaw != `{"x":1}` {
		t.Errorf("content_filter: %+v %v", resp, err)
	}
}

// TestCircuitOpen: after 5 retryable failures the primary's circuit opens
// and the next call goes to the fallback without touching the primary.
func TestCircuitOpen(t *testing.T) {
	t.Parallel()
	down := providertest.New(t, nil)
	for range retry.BreakerFailures {
		down.Push(providertest.Status(500, "", ""))
	}
	up := providertest.New(t, nil, providertest.Text("a"), providertest.Text("b"), providertest.Text("c"))
	o := client(t, provider.Config{
		Routes:   []provider.Route{route("m", down.URL), {Provider: "other", Model: "f", BaseURL: up.URL}},
		Fallback: []string{"other/f"},
	})
	// Call 1: 3 failures then fallback; call 2: failures 4–5 trip the
	// circuit, attempt 3 is refused locally; call 3: primary untouched.
	for _, want := range []string{"a", "b", "c"} {
		resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
		if err != nil || resp.Content != want || resp.FallbackFrom != "fake/m" {
			t.Fatalf("%+v %v", resp, err)
		}
	}
	if n := len(down.Requests()); n != retry.BreakerFailures {
		t.Errorf("primary got %d requests", n)
	}
}

func TestFixtureAndLatency(t *testing.T) {
	t.Parallel()
	clock := providertest.NewFakeClock()
	fx := providertest.Fixture(t, "testdata/openai-chat-tool.sse")
	fx.Delay = 10 * time.Second
	srv := providertest.New(t, clock, fx)
	o := client(t, provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
	var (
		resp provider.Response
		err  error
		wg   sync.WaitGroup
	)
	wg.Go(func() { resp, err = o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil) })
	clock.BlockUntil(1)
	clock.Advance(10 * time.Second)
	wg.Wait()
	if err != nil || resp.FinishReason != provider.FinishToolCalls || len(resp.ToolCalls) != 1 ||
		resp.ToolCalls[0].Name != "read_file" || resp.ToolCalls[0].ArgumentsRaw != `{"path":"go.mod"}` || resp.ReasoningText == "" {
		t.Errorf("%+v %v", resp, err)
	}
}

// TestEdgeCases (EDGE-CASES-hermes.md, ADR-077/080): EC-PROV entries of
// routing, fallback, and parameter handling.
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(t *testing.T)
	}{
		{"EC-PROV-011", ecTemperatureRejected},
		{"EC-PROV-012", ecReasoningEffortRejected},
		{"EC-PROV-015", ecFallbackSkipsBroken},
		{"EC-PROV-016", ecBackToPrimary},
		{"EC-PROV-025", ecFallbackWindow},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// ecTemperatureRejected: the route remembers the rejection; later calls
// never send temperature again (no repeated 400).
func ecTemperatureRejected(t *testing.T) {
	srv := providertest.New(t, nil, providertest.Status(400, "unsupported_parameter", "temperature"), providertest.Text("a"), providertest.Text("b"))
	o := client(t, provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
	temp := 0.3
	r := ask("fake/m")
	r.Temperature = &temp
	for range 2 {
		if resp, err := o.Do(t.Context(), provider.PurposeTurn, r, nil); err != nil || resp.Attempts != 1 {
			t.Fatalf("%+v %v", resp, err)
		}
	}
	reqs := srv.Requests()
	if len(reqs) != 3 || !strings.Contains(string(reqs[0]), `"temperature"`) || strings.Contains(string(reqs[1]), `"temperature"`) ||
		strings.Contains(string(reqs[2]), `"temperature"`) {
		t.Errorf("requests: %q", reqs)
	}
}

// ecReasoningEffortRejected: an endpoint naming reasoning_effort in
// error.param gets the request again without it.
func ecReasoningEffortRejected(t *testing.T) {
	srv := providertest.New(t, nil, providertest.Status(400, "invalid_request_error", "reasoning_effort"), providertest.Text("ok"))
	o := client(t, provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
	r := ask("fake/m")
	r.ReasoningEffort = "high"
	if resp, err := o.Do(t.Context(), provider.PurposeTurn, r, nil); err != nil || resp.Content != "ok" {
		t.Fatalf("%+v %v", resp, err)
	}
	if reqs := srv.Requests(); len(reqs) != 2 || strings.Contains(string(reqs[1]), "reasoning_effort") {
		t.Errorf("requests: %q", reqs)
	}
	// Without error.param the 400 stays a bad_request (no text parsing, E-21).
	srv.Push(providertest.Status(400, "invalid_request_error", ""))
	r.Temperature = new(0.1)
	if _, err := o.Do(t.Context(), provider.PurposeTurn, r, nil); retry.ClassifyErr(err) != retry.BadRequest {
		t.Errorf("400 without param: %v", err)
	}
}

// ecFallbackSkipsBroken: a fallback that cannot be activated (no route)
// does not block the next one.
func ecFallbackSkipsBroken(t *testing.T) {
	down := providertest.New(t, nil, providertest.Status(502, "", ""), providertest.Status(502, "", ""), providertest.Status(502, "", ""))
	up := providertest.New(t, nil, providertest.Text("saved"))
	var events []provider.Fallback
	o := client(t, provider.Config{
		Routes:     []provider.Route{route("m", down.URL), {Provider: "b", Model: "y", BaseURL: up.URL}},
		Fallback:   []string{"a/missing", "b/y"},
		OnFallback: func(f provider.Fallback) { events = append(events, f) },
	})
	resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if err != nil || resp.Content != "saved" || resp.FallbackFrom != "fake/m" || len(events) != 2 || events[0].To != "b/y" ||
		retry.ClassifyErr(events[0].Err) != retry.Server || events[0].Done || !events[1].Done || events[1].Outcome != nil {
		t.Errorf("%+v %v %+v", resp, err, events)
	}
}

// TestFallbackFailedVisible (PROV-M5, P7): a fallback that fails is still
// reported before its request and with its failure afterwards.
func TestFallbackFailedVisible(t *testing.T) {
	t.Parallel()
	five := func() []providertest.Reply {
		return []providertest.Reply{providertest.Status(500, "", ""), providertest.Status(500, "", ""), providertest.Status(500, "", "")}
	}
	prim := providertest.New(t, nil, five()...)
	fb := providertest.New(t, nil, five()...)
	var events []provider.Fallback
	o := client(t, provider.Config{
		Routes:     []provider.Route{route("m", prim.URL), {Provider: "b", Model: "f", BaseURL: fb.URL}},
		Fallback:   []string{"b/f"},
		OnFallback: func(f provider.Fallback) { events = append(events, f) },
	})
	_, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
	if retry.ClassifyErr(err) != retry.Server || len(fb.Requests()) != 3 {
		t.Fatalf("%v, %d fallback requests", err, len(fb.Requests()))
	}
	if len(events) != 2 || events[0].Done || events[0].To != "b/f" || !events[1].Done ||
		retry.ClassifyErr(events[1].Outcome) != retry.Server {
		t.Errorf("events %+v", events)
	}
}

// ecBackToPrimary: every call starts at the primary, so a session returns
// to it as soon as it recovers.
func ecBackToPrimary(t *testing.T) {
	prim := providertest.New(t, nil, providertest.Status(503, "", ""), providertest.Status(503, "", ""), providertest.Status(503, "", ""),
		providertest.Text("primary"))
	fb := providertest.New(t, nil, providertest.Text("fallback"))
	o := client(t, provider.Config{Routes: []provider.Route{route("m", prim.URL), route("f", fb.URL)}, Fallback: []string{"fake/f"}})
	for _, want := range []string{"fallback", "primary"} {
		if resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil || resp.Content != want {
			t.Fatalf("want %s: %+v %v", want, resp, err)
		}
	}
}

// ecFallbackWindow: a fallback whose context window is smaller than the
// request is skipped.
func ecFallbackWindow(t *testing.T) {
	prim := providertest.New(t, nil, providertest.Status(500, "", ""), providertest.Status(500, "", ""), providertest.Status(500, "", ""))
	big := providertest.New(t, nil, providertest.Text("big"))
	small := route("small", prim.URL)
	small.ContextWindow = 1000
	o := client(t, provider.Config{
		Routes:   []provider.Route{route("m", prim.URL), small, route("big", big.URL)},
		Fallback: []string{"fake/small", "fake/big"},
	})
	r := ask("fake/m")
	r.Messages[0].Content = strings.Repeat("word ", 2000) // ≈ 2500 tokens raw
	if resp, err := o.Do(t.Context(), provider.PurposeTurn, r, nil); err != nil || resp.Content != "big" {
		t.Errorf("%+v %v", resp, err)
	}
	if n := len(prim.Requests()); n != 3 {
		t.Errorf("small-window fallback was tried: %d requests on its server", n)
	}
}
