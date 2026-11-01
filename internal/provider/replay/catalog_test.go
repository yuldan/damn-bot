//go:build !minimal

package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/anthropic"
	"github.com/damnbot/damnbot/internal/provider/gemini"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/provider/responses"
)

// This file is the F5 provider fixture replay: every row of the embedded
// catalog (88 in PROVIDERS.md) is driven through the real wire adapter it
// names — openai-chat, openai-responses, anthropic-messages, or gemini — over
// a loopback HTTP server. Nothing is hand-copied per provider: the model,
// endpoint path, auth header, and expected body are derived from the catalog
// row, and the protocol fixtures are generated from the request model.
//
// Three catalog rows (bedrock, cursor, factory-droid) name a protocol without
// an adapter. Their fixture asserts the fail-closed startup error instead of
// pretending a replay happened; see TestCatalogReplayCoverage.

const (
	replayKey    = "sk-replay" // fake credential: never leaves the loopback listener
	replayText   = "replay-ping"
	replayReply  = "replay-ok"
	replayMaxTok = 64
	replayInput  = 11
	replayOutput = 4
	replayCached = 3
	replayTries  = 2 // one 429 plus one success
)

// protocolFixture is one recorded wire protocol: the endpoint path, the
// request-body shape, and the response stream. It is shared by every provider
// that speaks the protocol.
type protocolFixture struct {
	path   func(model string) string
	stream func(model string) string
	body   func(t *testing.T, raw []byte, model string)
}

// fixtures maps every adapter this build wires to its protocol fixture.
func fixtures() map[registry.Protocol]*protocolFixture {
	return map[registry.Protocol]*protocolFixture{
		registry.ProtocolOpenAIChat: {
			path:   func(string) string { return "/chat/completions" },
			stream: chatStream,
			body:   chatBody,
		},
		registry.ProtocolOpenAIResponses: {
			path:   func(string) string { return "/responses" },
			stream: responsesStream,
			body:   responsesBody,
		},
		registry.ProtocolAnthropicMessages: {
			path:   func(string) string { return "/v1/messages" },
			stream: anthropicStream,
			body:   anthropicBody,
		},
		registry.ProtocolGemini: {
			path: func(model string) string {
				return "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
			},
			stream: geminiStream,
			body:   geminiBody,
		},
	}
}

// replayProtocol returns the first catalog protocol with a wired adapter,
// matching provider.pickWire order.
func replayProtocol(f map[registry.Protocol]*protocolFixture, p *registry.Provider) (registry.Protocol, bool) {
	for _, proto := range p.Protocols {
		if f[proto] != nil {
			return proto, true
		}
	}
	return "", false
}

// replayModel is the deterministic model of one catalog row. The catalog has
// no per-provider default model (a model is always part of the reference), so
// the fixture uses a stable name derived from the id and asserts that exact
// name reaches the wire.
func replayModel(p *registry.Provider) string {
	return "replay-" + strings.ReplaceAll(p.ID, ".", "-")
}

// replayKeyOf is the credential the fixture sends: empty for auth=none (the
// transport must not invent one), otherwise one static fake key.
func replayKeyOf(p *registry.Provider) string {
	if p.Auth == registry.AuthNone {
		return ""
	}
	return replayKey
}

// wireMap wires the three non-default adapters; openai-chat is the provider
// package default and needs no entry.
func wireMap() map[registry.Protocol]provider.Wire {
	return map[registry.Protocol]provider.Wire{
		registry.ProtocolOpenAIResponses:   responses.New(),
		registry.ProtocolAnthropicMessages: anthropic.New(),
		registry.ProtocolGemini:            gemini.New(),
	}
}

func chatBody(t *testing.T, raw []byte, model string) {
	t.Helper()
	body := decodeMap(t, raw)
	want := map[string]any{"model": model, "stream": true, "max_tokens": float64(replayMaxTok)}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%q] = %v, want %v (%s)", k, body[k], v, raw)
		}
	}
	checkMessageText(t, body, "messages")
}

func responsesBody(t *testing.T, raw []byte, model string) {
	t.Helper()
	body := decodeMap(t, raw)
	want := map[string]any{"model": model, "stream": true, "store": false, "max_output_tokens": float64(replayMaxTok)}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%q] = %v, want %v (%s)", k, body[k], v, raw)
		}
	}
	checkInputText(t, body, "input")
}

func anthropicBody(t *testing.T, raw []byte, model string) {
	t.Helper()
	body := decodeMap(t, raw)
	want := map[string]any{"model": model, "stream": true, "max_tokens": float64(replayMaxTok)}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%q] = %v, want %v (%s)", k, body[k], v, raw)
		}
	}
	checkMessageText(t, body, "messages")
}

func geminiBody(t *testing.T, raw []byte, _ string) {
	t.Helper()
	body := decodeMap(t, raw)
	gc, ok := body["generationConfig"].(map[string]any)
	if !ok || gc["maxOutputTokens"] != float64(replayMaxTok) {
		t.Fatalf("generationConfig = %v (%s)", body["generationConfig"], raw)
	}
	if _, ok := body["model"]; ok {
		t.Fatalf("gemini body carries model; the model belongs in the path (%s)", raw)
	}
	if !strings.Contains(string(raw), replayText) {
		t.Fatalf("gemini contents lack %q (%s)", replayText, raw)
	}
	// The Gemini model lives in the path, never in the body; the request
	// path assertion covers it.
}

func decodeMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body %s: %v", raw, err)
	}
	return body
}

// checkMessageText asserts the chat/anthropic messages array carries the
// user turn verbatim. openai-chat sends a plain string; anthropic-messages
// sends content blocks.
func checkMessageText(t *testing.T, body map[string]any, key string) {
	t.Helper()
	msgs, ok := body[key].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("body[%q] = %v, want a non-empty array", key, body[key])
	}
	first, ok := msgs[0].(map[string]any)
	if !ok {
		t.Fatalf("body[%q][0] is not an object: %v", key, msgs[0])
	}
	if first["content"] != replayText && !strings.Contains(fmt.Sprint(first["content"]), replayText) {
		t.Fatalf("body[%q][0] = %v, want user/%q", key, first, replayText)
	}
	if first["role"] != "user" {
		t.Fatalf("body[%q][0] role = %v, want user", key, first["role"])
	}
}

// checkInputText asserts the Responses input array carries the user turn.
func checkInputText(t *testing.T, body map[string]any, key string) {
	t.Helper()
	items, ok := body[key].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("body[%q] = %v, want a non-empty array", key, body[key])
	}
	if !strings.Contains(fmt.Sprint(items[0]), replayText) {
		t.Fatalf("body[%q][0] = %v, want %q", key, items[0], replayText)
	}
}

// chatStream is the recorded openai-chat success stream (text, finish, usage).
func chatStream(model string) string {
	return fmt.Sprintf(`data: {"id":"chatcmpl-replay","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant","content":%q},"finish_reason":null}]}

data: {"id":"chatcmpl-replay","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-replay","object":"chat.completion.chunk","created":1,"model":%q,"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d}}}

data: [DONE]

`, model, replayReply, model, model, replayInput, replayOutput, replayInput+replayOutput, replayCached)
}

// responsesStream is the recorded openai-responses success stream.
func responsesStream(model string) string {
	return fmt.Sprintf(`data: {"type":"response.created","response":{"id":"resp_replay","model":%q,"status":"in_progress"}}

data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":%q}

data: {"type":"response.completed","response":{"id":"resp_replay","model":%q,"status":"completed","output":[],"usage":{"input_tokens":%d,"output_tokens":%d,"input_tokens_details":{"cached_tokens":%d}}}}

`, model, replayReply, model, replayInput, replayOutput, replayCached)
}

// anthropicStream is the recorded anthropic-messages success stream. Input is
// split as 8 uncached + 3 cache-read so the adapter reports 11 total.
func anthropicStream(model string) string {
	return fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"msg_replay","model":%q,"usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":%d,"cache_read_input_tokens":%d}}

event: message_stop
data: {"type":"message_stop"}

`, model, replayInput-replayCached, replayCached, replayReply, replayOutput, replayCached)
}

// geminiStream is the recorded gemini generateContent success stream.
func geminiStream(model string) string {
	return fmt.Sprintf(`data: {"candidates":[{"content":{"parts":[{"text":%q}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d,"cachedContentTokenCount":%d,"totalTokenCount":%d},"modelVersion":%q}

`, replayReply, replayInput, replayOutput, replayCached, replayInput+replayOutput, model)
}

// errorBody is a protocol-shaped non-2xx body; classification comes from the
// status, and the replay asserts that no protocol leaks a 400 into a retry.
func errorBody(proto registry.Protocol) string {
	switch proto {
	case registry.ProtocolAnthropicMessages:
		return `{"type":"error","error":{"type":"invalid_request_error","message":"replay bad"}}`
	case registry.ProtocolGemini:
		return `{"error":{"code":400,"message":"replay bad","status":"INVALID_ARGUMENT"}}`
	case registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses,
		registry.ProtocolBedrock, registry.ProtocolCursor, registry.ProtocolFactory,
		registry.ProtocolOllama, registry.ProtocolApple:
		return `{"error":{"message":"replay bad","type":"invalid_request_error","code":"invalid_request"}}`
	default:
		return `{"error":{"message":"replay bad","type":"invalid_request_error","code":"invalid_request"}}`
	}
}

// replayServer is one loopback endpoint for every catalog row. The first two
// path segments select the mode and the catalog id, so a single listener
// replays 85 providers without any per-provider test data file:
//
//	/<mode>/<provider-id>/<wire-path>
type replayServer struct {
	srv  *httptest.Server
	rows map[string]*registry.Provider
	fix  map[registry.Protocol]*protocolFixture

	mu   sync.Mutex
	hits map[string]int
	reqs []replayRequest
}

type replayRequest struct {
	mode, provider string
	path           string
	header         http.Header
	body           []byte
}

func newReplayServer(t *testing.T, cat *registry.Catalog) *replayServer {
	t.Helper()
	providers := cat.Providers()
	s := &replayServer{
		rows: make(map[string]*registry.Provider, len(providers)),
		fix:  fixtures(), hits: map[string]int{},
	}
	for i := range providers {
		if !providers[i].Local {
			s.rows[providers[i].ID] = &providers[i]
		}
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *replayServer) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, errorBody(registry.ProtocolOpenAIChat), http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		http.Error(w, `{"error":{"message":"bad replay path"}}`, http.StatusNotFound)
		return
	}
	mode, id := parts[0], parts[1]
	p, ok := s.rows[id]
	if !ok {
		http.Error(w, `{"error":{"message":"unknown replay provider"}}`, http.StatusNotFound)
		return
	}
	proto, ok := replayProtocol(s.fix, p)
	if !ok {
		http.Error(w, `{"error":{"message":"no adapter"}}`, http.StatusNotImplemented)
		return
	}
	s.mu.Lock()
	s.hits[mode+"/"+id]++
	attempt := s.hits[mode+"/"+id]
	s.reqs = append(s.reqs, replayRequest{
		mode: mode, provider: id, path: r.URL.RequestURI(),
		header: r.Header.Clone(), body: body,
	})
	s.mu.Unlock()

	if mode == "error" || (mode == "retry" && attempt == 1) {
		status := http.StatusBadRequest
		if mode == "retry" {
			status = http.StatusTooManyRequests
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if _, werr := io.WriteString(w, errorBody(proto)); werr != nil {
			return
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if _, werr := io.WriteString(w, s.fix[proto].stream(replayModel(p))); werr != nil {
		return
	}
}

// take returns and clears the requests recorded so far.
func (s *replayServer) take() []replayRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.reqs
	s.reqs = nil
	return out
}

// replayRow is one catalog row with the adapter it must use.
type replayRow struct {
	provider registry.Provider
	proto    registry.Protocol
}

// replayRows selects the wired rows in catalog order. The caller asserts the
// count so a new adapter-less row cannot silently drop out of the gate.
func replayRows(t *testing.T, cat *registry.Catalog, fix map[registry.Protocol]*protocolFixture) []replayRow {
	t.Helper()
	providers := cat.Providers()
	rows := make([]replayRow, 0, cat.ProviderCount())
	for i := range providers {
		p := &providers[i]
		if p.Local {
			continue
		}
		proto, ok := replayProtocol(fix, p)
		if !ok {
			continue
		}
		rows = append(rows, replayRow{provider: *p, proto: proto})
	}
	return rows
}

// replayRoute builds the route the runtime would build: the catalog protocol,
// the catalog credential kind, and a loopback base carrying mode + row id.
func replayRoute(srv *replayServer, row *replayRow, mode string) provider.Route {
	return provider.Route{
		Provider:  row.provider.ID,
		Model:     replayModel(&row.provider),
		BaseURL:   srv.srv.URL + "/" + mode + "/" + row.provider.ID,
		APIKey:    replayKeyOf(&row.provider),
		Protocols: []registry.Protocol{row.proto},
		UserAgent: "damnbot/replay",
	}
}

func replayClient(t *testing.T, routes []provider.Route, attempts int) *provider.OpenAI {
	t.Helper()
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	o, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: routes, Attempts: attempts, UserAgent: "damnbot/replay",
		Wires: wireMap(), Wait: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return o
}

func replayRequestFor(rt *provider.Route) *provider.Request {
	return &provider.Request{
		Model:     rt.Ref(),
		MaxTokens: replayMaxTok,
		Messages:  []provider.Message{{Role: "user", Content: replayText}},
	}
}

// TestCatalogReplaySuccess drives every wired catalog row through a 2xx
// stream and asserts the decoded text, finish reason, model, and usage.
func TestCatalogReplaySuccess(t *testing.T) {
	t.Parallel()
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	srv := newReplayServer(t, cat)
	rows := replayRows(t, cat, srv.fix)
	if len(rows) < 85 {
		t.Fatalf("replay rows = %d, want at least 85", len(rows))
	}
	routes := make([]provider.Route, len(rows))
	for i := range rows {
		routes[i] = replayRoute(srv, &rows[i], "ok")
	}
	o := replayClient(t, routes, 0)
	for i := range rows {
		row, rt := &rows[i], &routes[i]
		t.Run(row.provider.ID, func(t *testing.T) { replaySuccess(t, o, srv, row, rt) })
	}
}

func replaySuccess(t *testing.T, o *provider.OpenAI, srv *replayServer, row *replayRow, rt *provider.Route) {
	t.Helper()
	resp, err := o.Do(t.Context(), provider.PurposeTurn, replayRequestFor(rt), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	model := replayModel(&row.provider)
	if resp.Content != replayReply || resp.FinishReason != provider.FinishStop || resp.Model != model {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Usage.Input != replayInput || resp.Usage.Output != replayOutput || resp.Usage.CachedInput != replayCached {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if resp.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", resp.Attempts)
	}
	seen := srv.take()
	if len(seen) != 1 {
		t.Fatalf("%d requests, want 1", len(seen))
	}
	checkReplayRequest(t, &seen[0], row, model)
}

// TestCatalogReplayError asserts a 400 on every wired catalog row is typed
// non-retryable and sent exactly once.
func TestCatalogReplayError(t *testing.T) {
	t.Parallel()
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	srv := newReplayServer(t, cat)
	rows := replayRows(t, cat, srv.fix)
	routes := make([]provider.Route, len(rows))
	for i := range rows {
		routes[i] = replayRoute(srv, &rows[i], "error")
	}
	o := replayClient(t, routes, 0)
	for i := range rows {
		row, rt := &rows[i], &routes[i]
		t.Run(row.provider.ID, func(t *testing.T) { replayError(t, o, srv, rt) })
	}
}

func replayError(t *testing.T, o *provider.OpenAI, srv *replayServer, rt *provider.Route) {
	t.Helper()
	_, err := o.Do(t.Context(), provider.PurposeTurn, replayRequestFor(rt), nil)
	var pe *provider.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want *provider.Error", err)
	}
	if pe.HTTPStatus != http.StatusBadRequest || pe.Class != retry.BadRequest {
		t.Fatalf("error = %+v", pe)
	}
	if n := len(srv.take()); n != 1 {
		t.Fatalf("%d requests, want 1 (a 400 is not retried)", n)
	}
}

// TestCatalogReplayRetry asserts a 429 on every wired catalog row retries
// once and succeeds on the same route.
func TestCatalogReplayRetry(t *testing.T) {
	t.Parallel()
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	srv := newReplayServer(t, cat)
	rows := replayRows(t, cat, srv.fix)
	routes := make([]provider.Route, len(rows))
	for i := range rows {
		routes[i] = replayRoute(srv, &rows[i], "retry")
	}
	o := replayClient(t, routes, replayTries)
	for i := range rows {
		row, rt := &rows[i], &routes[i]
		t.Run(row.provider.ID, func(t *testing.T) { replayRetry(t, o, srv, rt) })
	}
}

func replayRetry(t *testing.T, o *provider.OpenAI, srv *replayServer, rt *provider.Route) {
	t.Helper()
	resp, err := o.Do(t.Context(), provider.PurposeTurn, replayRequestFor(rt), nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Content != replayReply || resp.Attempts != replayTries {
		t.Fatalf("response = %+v", resp)
	}
	if n := len(srv.take()); n != replayTries {
		t.Fatalf("%d requests, want %d", n, replayTries)
	}
}

// checkReplayRequest asserts the wire facts shared by all four adapters:
// adapter path, transport auth, SEC-25 User-Agent, and request encoding.
func checkReplayRequest(t *testing.T, req *replayRequest, row *replayRow, model string) {
	t.Helper()
	path := fixtures()[row.proto].path(model)
	if !strings.HasSuffix(req.path, path) {
		t.Fatalf("path = %q, want suffix %q", req.path, path)
	}
	if got := req.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := req.header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
	if got := req.header.Get("User-Agent"); got != "damnbot/replay" {
		t.Fatalf("User-Agent = %q", got)
	}
	checkReplayAuth(t, req, row)
	fixtures()[row.proto].body(t, req.body, model)
}

// checkReplayAuth asserts the exact credential header for the protocol and
// that no second, unintended credential header is present.
func checkReplayAuth(t *testing.T, req *replayRequest, row *replayRow) {
	t.Helper()
	key := replayKeyOf(&row.provider)
	switch row.proto {
	case registry.ProtocolAnthropicMessages:
		checkAnthropicAuth(t, req, key)
	case registry.ProtocolGemini:
		checkGeminiAuth(t, req, key)
	case registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses:
		checkBearerAuth(t, req, key)
	case registry.ProtocolBedrock, registry.ProtocolCursor, registry.ProtocolFactory,
		registry.ProtocolOllama, registry.ProtocolApple:
		t.Fatalf("protocol %s has no replay auth contract", row.proto)
	default:
		t.Fatalf("protocol %s has no replay auth contract", row.proto)
	}
	if row.provider.Auth == registry.AuthNone && key != "" {
		t.Fatal("auth=none row was given a credential")
	}
}

func checkAnthropicAuth(t *testing.T, req *replayRequest, key string) {
	t.Helper()
	if got := req.header.Get("anthropic-version"); got != anthropic.Version {
		t.Fatalf("anthropic-version = %q", got)
	}
	if got := req.header.Get("x-api-key"); got != key {
		t.Fatalf("x-api-key = %q, want %q", got, key)
	}
	if req.header.Get("Authorization") != "" || req.header.Get("x-goog-api-key") != "" {
		t.Fatal("anthropic request carried a foreign credential header")
	}
}

func checkGeminiAuth(t *testing.T, req *replayRequest, key string) {
	t.Helper()
	if got := req.header.Get("x-goog-api-key"); got != key {
		t.Fatalf("x-goog-api-key = %q, want %q", got, key)
	}
	if req.header.Get("Authorization") != "" || req.header.Get("x-api-key") != "" {
		t.Fatal("gemini request carried a foreign credential header")
	}
}

func checkBearerAuth(t *testing.T, req *replayRequest, key string) {
	t.Helper()
	want := ""
	if key != "" {
		want = "Bearer " + key
	}
	if got := req.header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
	if req.header.Get("x-api-key") != "" || req.header.Get("x-goog-api-key") != "" {
		t.Fatal("chat/responses request carried a non-bearer credential")
	}
}

// TestCatalogReplayBaseURLs asserts every wired catalog row with a default
// base yields a concrete https (or documented loopback) origin and the
// adapter path joins to a parseable request URL. A row whose base_url is
// empty is user-supplied by design (Azure/ACP/custom rows); the replay still
// reaches exactly the base the operator passes. Documented {region}/<account>
// placeholders must be substitutable, never sent literally.
func TestCatalogReplayBaseURLs(t *testing.T) {
	t.Parallel()
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	fix := fixtures()
	providers := cat.Providers()
	checked, userSupplied := 0, 0
	for i := range providers {
		p := &providers[i]
		if p.Local {
			continue
		}
		proto, ok := replayProtocol(fix, p)
		if !ok {
			continue
		}
		base := p.BaseURL
		if base == "" {
			userSupplied++
			continue
		}
		if strings.ContainsAny(base, "<>{") {
			base = strings.NewReplacer("{region}", "us-east-1", "<account>", "acct", "<gateway>", "gw").Replace(base)
		}
		norm, nerr := provider.NormalizeBaseURL(base)
		if nerr != nil {
			t.Fatalf("%s: base_url %q: %v", p.ID, p.BaseURL, nerr)
		}
		u, perr := url.Parse(norm + fix[proto].path(replayModel(p)))
		if perr != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			t.Fatalf("%s: request URL %q: %v", p.ID, norm, perr)
		}
		checked++
	}
	if checked+userSupplied != cat.ProviderCount()-3 {
		t.Fatalf("base URLs checked = %d (+%d user-supplied), want %d", checked, userSupplied, cat.ProviderCount()-3)
	}
	if userSupplied == 0 {
		t.Fatal("no user-supplied base_url row exercised the operator path")
	}
	t.Logf("base URLs: %d defaults validated, %d user-supplied replayed on loopback", checked, userSupplied)
}

// TestCatalogReplayCoverage proves the harness accounts for every catalog
// row: 85 rows speak a wired protocol, and the three remaining rows fail
// closed at construction (no adapter, never sent to the wrong encoder).
func TestCatalogReplayCoverage(t *testing.T) {
	t.Parallel()
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if cat.ProviderCount() < 88 {
		t.Fatalf("catalog has %d providers, want at least 88", cat.ProviderCount())
	}
	fix := fixtures()
	providers := cat.Providers()
	replayed := 0
	unsupported := map[string]bool{}
	perProtocol := map[registry.Protocol]int{}
	for i := range providers {
		p := &providers[i]
		if p.Local {
			continue
		}
		proto, ok := replayProtocol(fix, p)
		if !ok {
			unsupported[p.ID] = true
			checkUnsupportedFailsClosed(t, p)
			continue
		}
		replayed++
		perProtocol[proto]++
	}
	wantUnsupported := []string{"bedrock", "cursor", "factory-droid"}
	got := make([]string, 0, len(unsupported))
	for id := range unsupported {
		got = append(got, id)
	}
	slices.Sort(got)
	if !slices.Equal(got, wantUnsupported) {
		t.Fatalf("providers without a wired adapter = %v, want %v", got, wantUnsupported)
	}
	if replayed+len(unsupported) != cat.ProviderCount() {
		t.Fatalf("replayed %d + unsupported %d != %d", replayed, len(unsupported), cat.ProviderCount())
	}
	if replayed != 85 {
		t.Fatalf("replayed providers = %d, want 85", replayed)
	}
	for proto := range fix {
		if perProtocol[proto] == 0 {
			t.Fatalf("adapter %s replayed no provider", proto)
		}
	}
	t.Logf("replay coverage: %d wired + %d fail-closed = %d providers; chat=%d responses=%d anthropic=%d gemini=%d",
		replayed, len(unsupported), cat.ProviderCount(),
		perProtocol[registry.ProtocolOpenAIChat], perProtocol[registry.ProtocolOpenAIResponses],
		perProtocol[registry.ProtocolAnthropicMessages], perProtocol[registry.ProtocolGemini])
}

func checkUnsupportedFailsClosed(t *testing.T, p *registry.Provider) {
	t.Helper()
	_, err := provider.NewOpenAI(nil, &provider.Config{
		Routes: []provider.Route{{
			Provider: p.ID, Model: replayModel(p), BaseURL: "https://example.invalid/v1",
			Protocols: p.Protocols,
		}},
		Wires: wireMap(),
	})
	if !errors.Is(err, provider.ErrConfig) {
		t.Fatalf("%s (%v): construction error = %v, want ErrConfig", p.ID, p.Protocols, err)
	}
}
