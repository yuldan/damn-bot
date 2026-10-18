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
type DeltaDecoder struct {
	d   Delta
	sc  jscan
	out []byte
}

// Decode parses one data frame. Only choices[0] is read (n = 1).
func (dd *DeltaDecoder) Decode(frame []byte) (*Delta, error) {
	if cap(dd.out) < len(frame) {
		dd.out = make([]byte, 0, 2*len(frame))
	}
	dd.d = Delta{ToolCalls: dd.d.ToolCalls[:0]}
	dd.sc = jscan{b: frame, out: dd.out[:0]}
	s := &dd.sc
	err := s.object(func(key []byte) error {
		switch string(key) {
		case "choices":
			return s.array(func(i int) error {
				if i > 0 {
					_, err := s.skip()
					return err
				}
				return dd.choice()
			})
		case "usage":
			return dd.usage()
		case "model":
			return dd.optStr(&dd.d.Model)
		case "error":
			raw, err := s.skip()
			dd.d.Error = raw
			return err
		}
		_, err := s.skip()
		return err
	})
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
func (dd *DeltaDecoder) optInt() (int64, error) {
	if dd.sc.null() {
		return 0, nil
	}
	return dd.sc.int()
}

func (dd *DeltaDecoder) choice() error {
	s := &dd.sc
	return s.object(func(key []byte) error {
		switch string(key) {
		case "delta", "message":
			if s.null() {
				return nil
			}
			return s.object(dd.deltaField)
		case "finish_reason":
			return dd.optStr(&dd.d.Finish)
		}
		_, err := s.skip()
		return err
	})
}

func (dd *DeltaDecoder) deltaField(key []byte) error {
	s := &dd.sc
	switch string(key) {
	case "content":
		return dd.optStr(&dd.d.Content)
	case "reasoning_content", "reasoning":
		switch s.peek() {
		case '"':
			dd.d.ReasoningField = key
			return dd.optStr(&dd.d.Reasoning)
		case 'n':
			return dd.optStr(&dd.d.Reasoning)
		}
		raw, err := s.skip()
		dd.d.ReasoningRaw, dd.d.ReasoningField = raw, key
		return err
	case "tool_calls":
		if s.null() {
			return nil
		}
		return s.array(func(i int) error {
			if i == maxChunkToolDeltas {
				return ErrTooManyToolDeltas
			}
			return dd.toolCall()
		})
	}
	_, err := s.skip()
	return err
}

func (dd *DeltaDecoder) toolCall() error {
	s := &dd.sc
	var t ToolDelta
	err := s.object(func(key []byte) error {
		switch string(key) {
		case "index":
			n, err := s.int()
			t.Index, t.HasIndex = int(n), true
			return err
		case "id":
			return dd.optStr(&t.ID)
		case "function":
			return s.object(func(k []byte) error {
				switch string(k) {
				case "name":
					return dd.optStr(&t.Name)
				case "arguments":
					return dd.optStr(&t.Args)
				}
				_, err := s.skip()
				return err
			})
		}
		_, err := s.skip()
		return err
	})
	dd.d.ToolCalls = append(dd.d.ToolCalls, t)
	return err
}

func (dd *DeltaDecoder) usage() error {
	s := &dd.sc
	if s.null() {
		return nil
	}
	u := &dd.d.Usage
	u.Present = true
	return s.object(func(key []byte) error {
		var err error
		switch string(key) {
		case "prompt_tokens":
			u.Prompt, err = dd.optInt()
		case "completion_tokens":
			u.Output, err = dd.optInt()
		case "cost":
			if s.null() {
				return nil
			}
			u.Cost, err = s.number()
			if math.IsInf(u.Cost, 0) {
				err = errJSON // out of float64 range (encoding/json rejects it too)
			}
			u.HasCost = err == nil
		case "prompt_tokens_details":
			if s.null() {
				return nil
			}
			return s.object(func(k []byte) error {
				if string(k) == "cached_tokens" {
					var e error
					u.Cached, e = dd.optInt()
					return e
				}
				_, e := s.skip()
				return e
			})
		default:
			_, err = s.skip()
		}
		return err
	})
}
