package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// assemble feeds frames through the decoder and assembler.
func assemble(t *testing.T, frames ...string) (Response, error) {
	t.Helper()
	var dd DeltaDecoder
	var a assembler
	for _, f := range frames {
		d, err := dd.Decode([]byte(f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if err := a.add(d); err != nil {
			return Response{}, err
		}
	}
	return a.result()
}

func TestAssemble(t *testing.T) {
	t.Parallel()
	r, err := assemble(t,
		`{"model":"m-2026","choices":[{"delta":{"content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4},"cost":0.5}}`)
	if err != nil || r.Content != "Hello" || r.FinishReason != FinishStop || r.Model != "m-2026" ||
		r.Usage.Input != 10 || r.Usage.CachedInput != 4 || r.Usage.Output != 2 || r.Usage.CostUSD == nil || *r.Usage.CostUSD != 0.5 {
		t.Errorf("%+v %v", r, err)
	}
}

func TestFinish(t *testing.T) {
	t.Parallel()
	if _, err := assemble(t, `{"choices":[{"delta":{"content":"cut"}}]}`); !errors.Is(err, ErrStreamBroken) || retry.ClassifyErr(err) != retry.StreamBroken {
		t.Errorf("no finish: %v", err)
	}
	for in, want := range map[string]string{
		"tool_calls": FinishToolCalls, "function_call": FinishToolCalls, "tool_use": FinishToolCalls,
		"length": FinishLength, "max_tokens": FinishLength, "content_filter": FinishContentFilter, "eos": FinishStop, "stop": FinishStop,
	} {
		if got := normalizeFinish(in); got != want {
			t.Errorf("normalizeFinish(%q) = %q", in, got)
		}
	}
	// content_filter keeps partial content and tool calls for the loop (ADR-082).
	r, err := assemble(t, `{"choices":[{"delta":{"content":"par","tool_calls":[{"index":0,"id":"c","function":{"name":"x","arguments":"{}"}}]},"finish_reason":"content_filter"}]}`)
	if err != nil || r.FinishReason != FinishContentFilter || r.Content != "par" || len(r.ToolCalls) != 1 {
		t.Errorf("content_filter: %+v %v", r, err)
	}
}

func TestEncode(t *testing.T) {
	t.Parallel()
	temp := 0.2
	rt := &Route{Provider: "p", Model: "m", BaseURL: "http://x", UsageInclude: true}
	if rt.Ref() != "p/m" {
		t.Errorf("Ref = %q", rt.Ref())
	}
	req := &Request{
		Messages: []Message{
			{Role: "system", Content: "be <brief> & exact"},
			{
				Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", ArgumentsRaw: `{"path":"a"}`}},
				Reasoning: json.RawMessage(`[{"type":"thinking","thinking":"hm"}]`), ReasoningField: "reasoning_content",
			},
			{Role: "tool", ToolCallID: "c1", Content: "data"},
		},
		Tools:       []ToolSchema{{Name: "read_file", Description: "Read", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Temperature: &temp, ReasoningEffort: "low",
	}
	a, err := encode(rt, req, nil)
	if err != nil || !json.Valid(a) {
		t.Fatalf("%s %v", a, err)
	}
	if b, berr := encode(rt, req, nil); berr != nil || !bytes.Equal(a, b) {
		t.Error("encoding not deterministic (P15)")
	}
	for _, want := range []string{
		`"model":"m"`, `"max_tokens":8192`, `"stream":true`, `"stream_options":{"include_usage":true}`,
		`"temperature":0.2`, `"reasoning_effort":"low"`, `"usage":{"include":true}`, `be <brief> & exact`,
		`"reasoning_content":[{"type":"thinking","thinking":"hm"}]`, `"tool_call_id":"c1"`, `"arguments":"{\"path\":\"a\"}"`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("body lacks %s:\n%s", want, a)
		}
	}
	c, err := encode(rt, req, map[string]bool{paramTemperature: true, paramReasoningEffort: true})
	if err != nil || strings.Contains(string(c), "temperature") || strings.Contains(string(c), "reasoning_effort") {
		t.Errorf("dropped params still sent: %s %v", c, err)
	}
	req.Messages[1].Reasoning = json.RawMessage(`{bad`)
	if _, err := encode(rt, req, nil); err == nil {
		t.Error("invalid reasoning accepted")
	}
}

func statusErr(status int, body string, hdr http.Header) error {
	return &netx.StatusError{StatusCode: status, Header: hdr, Body: []byte(body)}
}

func TestToError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err         error
		class       retry.Class
		code, param string
	}{
		{statusErr(429, `{"error":{"code":"invalid_api_key","message":"please log in"}}`, http.Header{"Retry-After": {"3"}}), retry.RateLimited, "invalid_api_key", ""},
		{statusErr(400, `{"error":{"code":"context_length_exceeded","type":"invalid_request_error"}}`, nil), retry.ContextLength, "context_length_exceeded", ""},
		{statusErr(400, `{"error":{"code":null,"type":"invalid_request_error","param":"temperature"}}`, nil), retry.BadRequest, "invalid_request_error", "temperature"},
		{statusErr(503, `{"error":{"code":503}}`, nil), retry.Overloaded, "503", ""},
		{statusErr(401, `not json`, nil), retry.Auth, "", ""},
		{netx.ErrConnectionLost, retry.Unclassified, "", ""},
	}
	for _, c := range cases {
		e := toError(c.err)
		if e.Class != c.class || e.Code != c.code || e.Param != c.param || retry.ClassifyErr(e) != c.class {
			t.Errorf("%v → %+v", c.err, e)
		}
	}
	if e := toError(cases[0].err); e.RetryAfter() != 3e9 || !strings.Contains(e.Error(), "HTTP 429 invalid_api_key") {
		t.Errorf("retry-after/message: %v %v", e.RetryAfter(), e)
	}
}

// TestEdgeCases (EDGE-CASES-hermes.md, ADR-077/080): EC-PROV entries
// settled by the stream assembler and the request encoder.
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(t *testing.T)
	}{
		{"EC-PROV-001", ecToolFragments},
		{"EC-PROV-004", ecReasoningReplay},
		{"EC-PROV-005", ecThinkingBlocksReplay},
		{"EC-PROV-006", ecReasoningArray},
		{"EC-PROV-007", ecEmptyContent},
		{"EC-PROV-013", ecControlTokens},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// ecToolFragments: LM Studio/Kimi/MiniMax-style fragments (repeated index,
// repeated id with a new index, id-less continuations) form exactly one call.
func ecToolFragments(t *testing.T) {
	r, err := assemble(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"edit_","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"file","arguments":"th\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_9","function":{"arguments":"\"a.go\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"function":{"arguments":"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	if err != nil || len(r.ToolCalls) != 1 || r.ToolCalls[0] != (ToolCall{ID: "call_9", Name: "edit_file", ArgumentsRaw: `{"path":"a.go"}`}) {
		t.Errorf("%+v %v", r.ToolCalls, err)
	}
	r, err = assemble(t,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"x","arguments":"{}"}},{"index":1,"id":"b","function":{"name":"y","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	if err != nil || len(r.ToolCalls) != 2 {
		t.Errorf("parallel calls merged: %+v %v", r.ToolCalls, err)
	}
}

// ecReasoningReplay: reasoning_content of an assistant tool-call turn is
// returned and replayed under the same field.
func ecReasoningReplay(t *testing.T) {
	r, err := assemble(t, `{"choices":[{"delta":{"reasoning_content":"plan: "}}]}`, `{"choices":[{"delta":{"reasoning_content":"read"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	if err != nil || string(r.Reasoning) != `"plan: read"` || r.ReasoningField != "reasoning_content" || r.ReasoningText != "plan: read" {
		t.Fatalf("%+v %v", r, err)
	}
	body := replay(t, &r)
	if !strings.Contains(body, `"reasoning_content":"plan: read"`) {
		t.Errorf("replay drops reasoning: %s", body)
	}
}

// ecThinkingBlocksReplay: typed thinking blocks go back byte-identical.
func ecThinkingBlocksReplay(t *testing.T) {
	blocks := `[{"type":"thinking","thinking":"x","signature":"sig=="}]`
	r, err := assemble(t, `{"choices":[{"delta":{"reasoning":`+blocks+`,"content":"ok"},"finish_reason":"stop"}]}`)
	if err != nil || string(r.Reasoning) != `[`+blocks+`]` || r.ReasoningField != "reasoning" {
		t.Fatalf("%s %v", r.Reasoning, err)
	}
	if body := replay(t, &r); !strings.Contains(body, `"reasoning":[`+blocks+`]`) {
		t.Errorf("thinking blocks altered on replay: %s", body)
	}
}

func replay(t *testing.T, r *Response) string {
	t.Helper()
	b, err := encode(&Route{Model: "m"}, &Request{Messages: []Message{{
		Role: "assistant", Content: r.Content,
		ToolCalls: r.ToolCalls, Reasoning: r.Reasoning, ReasoningField: r.ReasoningField,
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ecReasoningArray: list-shaped thinking normalizes to text.
func ecReasoningArray(t *testing.T) {
	r, err := assemble(t, `{"choices":[{"delta":{"reasoning_content":"a"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":[{"type":"text","text":"b"},"c"]}}]}`,
		`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`)
	if err != nil || r.ReasoningText != "abc" || string(r.Reasoning) != `["a",[{"type":"text","text":"b"},"c"]]` {
		t.Errorf("%q %s %v", r.ReasoningText, r.Reasoning, err)
	}
}

// ecEmptyContent: HTTP 200 with empty content (also thinking-only) is a
// failure of class empty_response, never a valid final answer.
func ecEmptyContent(t *testing.T) {
	for _, frames := range [][]string{
		{`{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`},
		{`{"choices":[{"delta":{"reasoning_content":"hmm","content":"  \n"},"finish_reason":"stop"}]}`},
	} {
		if _, err := assemble(t, frames...); !errors.Is(err, ErrEmptyResponse) || retry.ClassifyErr(err) != retry.EmptyResponse {
			t.Errorf("%v: %v", frames, err)
		}
	}
}

// ecControlTokens: EOS tokens are stripped from the end of the final text
// only.
func ecControlTokens(t *testing.T) {
	cases := map[string]string{
		"Done.<|im_end|>":                 "Done.",
		"Done. <|eot_id|>\n<|endoftext|>": "Done.",
		"keep </s> inside":                "keep </s> inside",
		"plain\n":                         "plain\n",
		"x</s></s> ":                      "x",
	}
	for in, want := range cases {
		if got := stripControl(in); got != want {
			t.Errorf("stripControl(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAssemblyBounds: a hostile endpoint cannot grow a response beyond
// maxResponseBytes or maxToolCalls (not retried).
func TestAssemblyBounds(t *testing.T) {
	t.Parallel()
	var a assembler
	chunk := bytes.Repeat([]byte("x"), 4<<20)
	var err error
	for range 5 {
		if err = a.add(&Delta{Content: chunk}); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrResponseTooLarge) || retry.ClassifyErr(err).Retryable() {
		t.Errorf("content cap: %v", err)
	}
	var b assembler
	for i := range maxToolCalls + 1 {
		err = b.add(&Delta{ToolCalls: []ToolDelta{{Index: i, HasIndex: true, ID: []byte(fmt.Sprint("c", i)), Name: []byte("x")}}})
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("tool-call cap: %v", err)
	}
}
