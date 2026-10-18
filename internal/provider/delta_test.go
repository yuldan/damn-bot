package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
)

// absent reports a missing or null JSON value.
func absent(raw json.RawMessage) bool { return raw == nil || string(raw) == "null" }

// refStr decodes an optional string; ok=false on a non-string value.
func refStr(raw json.RawMessage, dst *[]byte) bool {
	if absent(raw) {
		return true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	*dst = []byte(s)
	return true
}

// refInt decodes an optional non-negative integral number.
func refInt(raw json.RawMessage, dst *int64) bool {
	if absent(raw) {
		return true
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || f < 0 || f != math.Trunc(f) || f > 1<<53 {
		return false
	}
	*dst = int64(f)
	return true
}

// refObj decodes an optional object (null = empty).
func refObj(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if absent(raw) {
		return m, true
	}
	return m, json.Unmarshal(raw, &m) == nil
}

// refDelta decodes a frame with encoding/json, matching keys exactly, for
// the differential fuzz target. ok=false: the decoder must reject it.
func refDelta(frame []byte) (d Delta, ok bool) {
	top, ok := refObj(frame)
	if !ok || !refStr(top["model"], &d.Model) || !refUsage(top["usage"], &d.Usage) {
		return d, false
	}
	if e, has := top["error"]; has {
		d.Error = e
	}
	var choices []json.RawMessage
	if c := top["choices"]; !absent(c) && json.Unmarshal(c, &choices) != nil {
		return d, false
	}
	if len(choices) == 0 {
		return d, true
	}
	choice, ok := refObj(choices[0])
	if !ok || !refStr(choice["finish_reason"], &d.Finish) {
		return d, false
	}
	body := choice["delta"]
	if m, has := choice["message"]; has {
		body = m
	}
	ok = refBody(body, &d)
	return d, ok
}

// refBody decodes a choice's delta/message object.
func refBody(body json.RawMessage, d *Delta) bool {
	delta, ok := refObj(body)
	if !ok || !refStr(delta["content"], &d.Content) {
		return false
	}
	for _, k := range []string{"reasoning_content", "reasoning"} {
		if r := delta[k]; !absent(r) && !refStr(r, &d.Reasoning) {
			d.Reasoning, d.ReasoningRaw = nil, r
		}
	}
	return refTools(delta["tool_calls"], d)
}

func refUsage(raw json.RawMessage, u *DeltaUsage) bool {
	if absent(raw) {
		return true
	}
	m, ok := refObj(raw)
	if !ok || !refInt(m["prompt_tokens"], &u.Prompt) || !refInt(m["completion_tokens"], &u.Output) {
		return false
	}
	u.Present = true
	if c := m["cost"]; !absent(c) {
		if json.Unmarshal(c, &u.Cost) != nil {
			return false
		}
		u.HasCost = true
	}
	det, ok := refObj(m["prompt_tokens_details"])
	return ok && refInt(det["cached_tokens"], &u.Cached)
}

func refTools(raw json.RawMessage, d *Delta) bool {
	var calls []json.RawMessage
	if !absent(raw) && json.Unmarshal(raw, &calls) != nil {
		return false
	}
	for _, rc := range calls {
		var t ToolDelta
		c, ok := refObj(rc)
		if !ok {
			return false
		}
		if ix := c["index"]; !absent(ix) {
			var n int64
			if !refInt(ix, &n) {
				return false
			}
			t.Index, t.HasIndex = int(n), true
		}
		fn, ok := refObj(c["function"])
		if !ok || !refStr(fn["name"], &t.Name) || !refStr(fn["arguments"], &t.Args) || !refStr(c["id"], &t.ID) {
			return false
		}
		d.ToolCalls = append(d.ToolCalls, t)
	}
	return true
}

func sameUsage(a, b DeltaUsage) bool {
	return a.Present == b.Present && a.Prompt == b.Prompt && a.Output == b.Output && a.Cached == b.Cached &&
		a.HasCost == b.HasCost && math.Abs(a.Cost-b.Cost) <= 1e-9*math.Max(1, math.Abs(b.Cost))
}

func sameTool(x, y ToolDelta) bool { //nolint:gocritic // hugeParam: slices.EqualFunc signature
	return x.Index == y.Index && x.HasIndex == y.HasIndex && bytes.Equal(x.ID, y.ID) &&
		bytes.Equal(x.Name, y.Name) && bytes.Equal(x.Args, y.Args)
}

func sameDelta(a, b *Delta) bool {
	if !bytes.Equal(a.Content, b.Content) || !bytes.Equal(a.Reasoning, b.Reasoning) || !bytes.Equal(a.Finish, b.Finish) ||
		!bytes.Equal(a.Model, b.Model) || (a.ReasoningRaw == nil) != (b.ReasoningRaw == nil) ||
		(a.Error == nil) != (b.Error == nil) || !sameUsage(a.Usage, b.Usage) {
		return false
	}
	return slices.EqualFunc(a.ToolCalls, b.ToolCalls, sameTool)
}

func deltaSamples() []string {
	return []string{
		`{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello, wo"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":"line\nwith \"quotes\" \u00e9 \ud83d\ude00"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning":[{"type":"text","text":"a"}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":null}`,
		`{"choices":[],"usage":{"prompt_tokens":1200,"completion_tokens":34,"prompt_tokens_details":{"cached_tokens":1024},"cost":0.000123}}`,
		`{"error":{"message":"overloaded","code":"server_error"}}`,
		`{"choices":[{"index":0,"message":{"content":"full"},"finish_reason":"stop"}]}`,
	}
}

func TestDeltaDecode(t *testing.T) {
	t.Parallel()
	var dd DeltaDecoder
	for _, f := range deltaSamples() {
		want, ok := refDelta([]byte(f))
		got, err := dd.Decode([]byte(f))
		if !ok || err != nil || !sameDelta(got, &want) {
			t.Errorf("%s:\n got %+v %v\nwant %+v", f, got, err, want)
		}
	}
	for _, bad := range []string{``, `{`, `{"choices":[}`, `{"a":01}`, `{"a":"\x01"}`, `{"a":"\q"}`, `{} x`, `{"usage":{"prompt_tokens":1.5}}`, `{"a":tru}`} {
		if _, err := dd.Decode([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestDeltaDecodeZeroAlloc (E-36): content, tool call, and usage chunks
// decode with 0 allocations after warmup.
func TestDeltaDecodeZeroAlloc(t *testing.T) {
	var dd DeltaDecoder
	frames := make([][]byte, 0, len(deltaSamples()))
	for _, f := range deltaSamples() {
		frames = append(frames, []byte(f))
	}
	for range 3 {
		for _, f := range frames {
			if _, err := dd.Decode(f); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := testing.AllocsPerRun(200, func() {
		for _, f := range frames {
			if _, err := dd.Decode(f); err != nil {
				t.Fatal(err)
			}
		}
	}); n != 0 {
		t.Errorf("allocs per %d chunks = %v, want 0", len(frames), n)
	}
}

func BenchmarkDeltaDecode(b *testing.B) {
	var dd DeltaDecoder
	frame := []byte(deltaSamples()[0])
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := dd.Decode(frame); err != nil {
			b.Fatal(err)
		}
	}
}

// FuzzDeltaDecode: differential against encoding/json; never panics.
func FuzzDeltaDecode(f *testing.F) {
	for _, s := range deltaSamples() {
		f.Add([]byte(s))
	}
	f.Add(deepFrame(maxDepth-1, false)) // PROV-B1: at the limit
	f.Add(deepFrame(maxDepth, false))   // PROV-B1: 10001 levels
	f.Add(deepFrame(maxDepth, true))
	f.Fuzz(func(t *testing.T, frame []byte) {
		var dd DeltaDecoder
		got, err := dd.Decode(frame)
		if errors.Is(err, ErrTooManyToolDeltas) {
			return // PROV-M2 bound: stricter than encoding/json by design
		}
		want, ok := refDelta(frame)
		if ok != (err == nil) {
			t.Fatalf("%q: decoder err=%v, encoding/json ok=%v", frame, err, ok)
		}
		if ok && !sameDelta(got, &want) {
			t.Fatalf("%q:\n got %+v\nwant %+v", frame, got, want)
		}
	})
}

// deepFrame nests n arrays (or objects) under an unknown key of the frame,
// so the decoder skips them: absolute nesting = n + 1.
func deepFrame(n int, obj bool) []byte {
	open, closeTok := "[", "]"
	if obj {
		open, closeTok = `{"a":`, "}"
	}
	inner := "1"
	if obj {
		inner = "null"
	}
	return []byte(`{"x":` + strings.Repeat(open, n) + inner + strings.Repeat(closeTok, n) + `}`)
}

// TestDeltaDeepNesting (PROV-B1): nesting is bounded like encoding/json
// (10000) without recursion; a maximal 4 MiB SSE frame of '[' is a clean
// errJSON, not a stack overflow.
func TestDeltaDeepNesting(t *testing.T) {
	t.Parallel()
	frames := map[string][]byte{
		"limit-array":      deepFrame(maxDepth-1, false),
		"over-array":       deepFrame(maxDepth, false),
		"limit-object":     deepFrame(maxDepth-1, true),
		"over-object":      deepFrame(maxDepth, true),
		"open-4MiB":        append([]byte(`{"x":`), bytes.Repeat([]byte("["), 4<<20-5)...),
		"open-4MiB-top":    bytes.Repeat([]byte("["), 4<<20),
		"open-4MiB-object": append([]byte(`{"x":`), bytes.Repeat([]byte(`{"a":`), (4<<20)/5-1)...),
		"mixed":            []byte(`{"x":[{"a":[1,{"b":[]},[[]]]},{}],"choices":[]}`),
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var dd DeltaDecoder
			_, err := dd.Decode(frame)
			_, ok := refDelta(frame)
			if ok != (err == nil) {
				t.Fatalf("decoder err=%v, encoding/json ok=%v", err, ok)
			}
			if strings.HasPrefix(name, "over") || strings.HasPrefix(name, "open") {
				if !errors.Is(err, errJSON) {
					t.Fatalf("want errJSON, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
