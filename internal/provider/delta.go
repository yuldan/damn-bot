package provider

import (
	"errors"
	"fmt"
	"math"
)

// ToolDelta is one tool_calls fragment of a streamed chunk.
type ToolDelta struct {
	Index    int
	HasIndex bool
	ID, Name []byte
	Args     []byte
}

// DeltaUsage is the usage block of a chunk (stream_options.include_usage).
type DeltaUsage struct {
	Present                bool
	Prompt, Output, Cached int64
	Cost                   float64
	HasCost                bool
}

// Delta is one decoded openai-chat stream chunk. Byte slices alias the SSE
// frame or the decoder's scratch buffer and stay valid until the next
// Decode (E-36).
type Delta struct {
	Content   []byte
	Reasoning []byte // string reasoning delta (reasoning_content or reasoning)
	// ReasoningRaw is a non-string reasoning value (array/object, EC-PROV-006).
	ReasoningRaw   []byte
	ReasoningField []byte
	ToolCalls      []ToolDelta
	Finish         []byte
	Model          []byte
	Usage          DeltaUsage
	Error          []byte // raw "error" object sent inside a 2xx stream
}

// maxChunkToolDeltas bounds the tool_calls entries of one chunk (PROV-M2):
// a 4 MiB frame of `{}` would otherwise decode into ~1.4 M fragments.
const maxChunkToolDeltas = 256

// ErrTooManyToolDeltas reports that a chunk carries more than maxChunkToolDeltas
// tool_calls entries; the client reports it as ErrResponseTooLarge.
var ErrTooManyToolDeltas = errors.New("too many tool_calls entries in one chunk")

// DeltaDecoder decodes chunks without reflection and, after warmup,
// without allocation (E-36). Not safe for concurrent use.
//
// Duplicate keys behave like encoding/json into a map: the last value of a
// key wins and replaces (not merges) what an earlier one decoded, and only
// the last value must have the expected shape (ADR-102 butir 14).
type DeltaDecoder struct {
	d     Delta
	sc    jscan
	out   []byte
	tools int // tool_calls entries decoded in this frame, across duplicates (PROV-M2)
}

// Decode parses one data frame. Only choices[0] is read (n = 1).
func (dd *DeltaDecoder) Decode(frame []byte) (*Delta, error) {
	if cap(dd.out) < len(frame) {
		dd.out = make([]byte, 0, 2*len(frame))
	}
	dd.d = Delta{ToolCalls: dd.d.ToolCalls[:0]}
	dd.sc = jscan{b: frame, out: dd.out[:0]}
	dd.tools = 0
	s := &dd.sc
	var bad uint8
	err := s.object(func(key []byte) error {
		switch string(key) {
		case "choices":
			return s.settle(s.mark(), dd.choices(), &bad, 1)
		case "usage":
			return s.settle(s.mark(), dd.usage(), &bad, 2)
		case "model":
			return s.settle(s.mark(), dd.optStr(&dd.d.Model), &bad, 4)
		case "error":
			raw, err := s.skip()
			dd.d.Error = raw
			return err
		}
		_, err := s.skip()
		return err
	})
	err = unresolved(err, bad)
	if s.ws(); err == nil && s.i != len(s.b) {
		err = errJSON
	}
	if err != nil {
		return nil, fmt.Errorf("delta: %w at byte %d", err, s.i)
	}
	return &dd.d, nil
}

func (dd *DeltaDecoder) optStr(dst *[]byte) error {
	if dd.sc.null() {
		*dst = nil
		return nil
	}
	v, err := dd.sc.str()
	*dst = v
	return err
}

// optInt reads a token count; null is 0.
func (dd *DeltaDecoder) optInt(dst *int64) error {
	if dd.sc.null() {
		*dst = 0
		return nil
	}
	v, err := dd.sc.int()
	*dst = v
	return err
}

// choices reads the choices array; a repeated key replaces choices[0].
func (dd *DeltaDecoder) choices() error {
	dd.d.Finish = nil
	dd.resetBody()
	s := &dd.sc
	return s.array(func(i int) error {
		if i > 0 {
			_, err := s.skip()
			return err
		}
		return dd.choice()
	})
}

// choice reads choices[0]. A "message" key takes precedence over "delta"
// wherever it appears (refDelta in delta_test.go).
func (dd *DeltaDecoder) choice() error {
	s := &dd.sc
	var bad uint8
	message := false
	err := s.object(func(key []byte) error {
		switch string(key) {
		case "delta":
			if !message {
				return s.settle(s.mark(), dd.body(), &bad, 1)
			}
		case "message":
			message, bad = true, bad&^1 // an earlier delta no longer counts
			return s.settle(s.mark(), dd.body(), &bad, 2)
		case "finish_reason":
			return s.settle(s.mark(), dd.optStr(&dd.d.Finish), &bad, 4)
		}
		_, err := s.skip()
		return err
	})
	return unresolved(err, bad)
}

func (dd *DeltaDecoder) resetBody() {
	d := &dd.d
	d.Content, d.Reasoning, d.ReasoningRaw, d.ReasoningField = nil, nil, nil, nil
	d.ToolCalls = d.ToolCalls[:0]
}

// reasoningVal is the last value of one reasoning key in a body.
type reasoningVal struct {
	key, str, raw []byte // str: a string value; raw: a non-string value
	isStr         bool
}

// body reads a delta/message object, replacing an earlier one.
func (dd *DeltaDecoder) body() error {
	dd.resetBody()
	s := &dd.sc
	var rs [2]reasoningVal // reasoning_content, reasoning
	var bad uint8
	err := s.object(func(key []byte) error {
		switch string(key) {
		case "content":
			return s.settle(s.mark(), dd.optStr(&dd.d.Content), &bad, 1)
		case "reasoning_content":
			return dd.reasoning(&rs[0], key)
		case "reasoning":
			return dd.reasoning(&rs[1], key)
		case "tool_calls":
			return s.settle(s.mark(), dd.toolCalls(), &bad, 2)
		}
		_, err := s.skip()
		return err
	})
	err = unresolved(err, bad)
	if err != nil {
		return err
	}
	// Both keys present: reasoning_content applies first, then reasoning
	// overrides it (a string keeps an earlier raw value, like refBody).
	d := &dd.d
	for i := range rs {
		switch r := &rs[i]; {
		case r.isStr:
			d.Reasoning, d.ReasoningField = r.str, r.key
		case r.raw != nil:
			d.Reasoning, d.ReasoningRaw, d.ReasoningField = nil, r.raw, r.key
		}
	}
	return nil
}

// reasoning reads a reasoning value of any type; null clears it.
func (dd *DeltaDecoder) reasoning(r *reasoningVal, key []byte) error {
	s := &dd.sc
	*r = reasoningVal{key: key}
	switch s.peek() {
	case '"':
		v, err := s.str()
		r.str, r.isStr = v, true
		return err
	case 'n':
		if s.null() {
			return nil
		}
	}
	raw, err := s.skip()
	r.raw = raw
	return err
}

// toolCalls reads a tool_calls array, replacing an earlier one; the entry
// count spans duplicates so they cannot reset the bound (PROV-M2).
func (dd *DeltaDecoder) toolCalls() error {
	dd.d.ToolCalls = dd.d.ToolCalls[:0]
	return dd.sc.array(func(int) error {
		if dd.tools == maxChunkToolDeltas {
			return ErrTooManyToolDeltas
		}
		dd.tools++
		return dd.toolCall()
	})
}

func (dd *DeltaDecoder) toolCall() error {
	s := &dd.sc
	var t ToolDelta
	var bad uint8
	err := s.object(func(key []byte) error {
		switch string(key) {
		case "index":
			return s.settle(s.mark(), dd.index(&t), &bad, 1)
		case "id":
			return s.settle(s.mark(), dd.optStr(&t.ID), &bad, 2)
		case "function":
			return s.settle(s.mark(), dd.function(&t), &bad, 4)
		}
		_, err := s.skip()
		return err
	})
	dd.d.ToolCalls = append(dd.d.ToolCalls, t)
	return unresolved(err, bad)
}

// index reads a tool call index; null is no index (encoding/json).
func (dd *DeltaDecoder) index(t *ToolDelta) error {
	t.Index, t.HasIndex = 0, false
	if dd.sc.null() {
		return nil
	}
	n, err := dd.sc.int()
	t.Index, t.HasIndex = int(n), true
	return err
}

func (dd *DeltaDecoder) function(t *ToolDelta) error {
	s := &dd.sc
	t.Name, t.Args = nil, nil
	var bad uint8
	err := s.object(func(k []byte) error {
		switch string(k) {
		case "name":
			return s.settle(s.mark(), dd.optStr(&t.Name), &bad, 1)
		case "arguments":
			return s.settle(s.mark(), dd.optStr(&t.Args), &bad, 2)
		}
		_, err := s.skip()
		return err
	})
	return unresolved(err, bad)
}

// usage reads the usage object, replacing an earlier one.
func (dd *DeltaDecoder) usage() error {
	s := &dd.sc
	u := &dd.d.Usage
	*u = DeltaUsage{}
	if s.null() {
		return nil
	}
	u.Present = true
	var bad uint8
	err := s.object(func(key []byte) error {
		switch string(key) {
		case "prompt_tokens":
			return s.settle(s.mark(), dd.optInt(&u.Prompt), &bad, 1)
		case "completion_tokens":
			return s.settle(s.mark(), dd.optInt(&u.Output), &bad, 2)
		case "cost":
			return s.settle(s.mark(), dd.cost(), &bad, 4)
		case "prompt_tokens_details":
			return s.settle(s.mark(), dd.details(), &bad, 8)
		}
		_, err := s.skip()
		return err
	})
	return unresolved(err, bad)
}

func (dd *DeltaDecoder) cost() error {
	s, u := &dd.sc, &dd.d.Usage
	u.Cost, u.HasCost = 0, false
	if s.null() {
		return nil
	}
	v, err := s.number()
	if math.IsInf(v, 0) {
		err = errJSON // out of float64 range (encoding/json rejects it too)
	}
	u.Cost, u.HasCost = v, err == nil
	return err
}

func (dd *DeltaDecoder) details() error {
	s, u := &dd.sc, &dd.d.Usage
	u.Cached = 0
	var bad uint8
	err := s.object(func(k []byte) error {
		if string(k) == "cached_tokens" {
			return s.settle(s.mark(), dd.optInt(&u.Cached), &bad, 1)
		}
		_, err := s.skip()
		return err
	})
	return unresolved(err, bad)
}
