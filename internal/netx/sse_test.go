package netx

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// refEvent is an owned copy of an Event for comparisons.
type refEvent struct{ typ, data, id string }

// refParse is a naive reference of WHATWG HTML §9.2.6 used by the table and
// the differential fuzz target.
func refParse(in string) (evs []refEvent, retry time.Duration) {
	in = strings.TrimPrefix(in, bom)
	retry = -1
	var data, typ, id string
	hasData := false
	for {
		i := strings.IndexAny(in, "\r\n")
		if i < 0 {
			return evs, retry // a trailing incomplete line is discarded
		}
		line := in[:i]
		if in[i] == '\r' && i+1 < len(in) && in[i+1] == '\n' {
			i++
		}
		in = in[i+1:]
		if line == "" {
			if hasData {
				evs = append(evs, refEvent{typ, strings.TrimSuffix(data, "\n"), id})
			}
			data, typ, hasData = "", "", false
			continue
		}
		data, typ, id, retry, hasData = refField(line, data, typ, id, retry, hasData)
	}
}

// refField applies one non-empty line to the reference state.
func refField(line, data, typ, id string, retry time.Duration, hasData bool) (string, string, string, time.Duration, bool) {
	name, value, found := strings.Cut(line, ":")
	if found && name == "" {
		return data, typ, id, retry, hasData // comment
	}
	value = strings.TrimPrefix(value, " ")
	switch name {
	case "data":
		data += value + "\n"
		hasData = true
	case "event":
		typ = value
	case "id":
		if !strings.Contains(value, "\x00") {
			id = value
		}
	case "retry":
		if d, ok := digits(value); ok {
			retry = d
		}
	}
	return data, typ, id, retry, hasData
}

func digits(v string) (time.Duration, bool) {
	if v == "" || len(v) > 12 {
		return 0, false
	}
	var ms int64
	for _, c := range []byte(v) {
		if c < '0' || c > '9' {
			return 0, false
		}
		ms = ms*10 + int64(c-'0')
	}
	return time.Duration(ms) * time.Millisecond, true
}

// parseAll drains a parser; io.EOF is success.
func parseAll(r io.Reader, maxEvent int) ([]refEvent, time.Duration, error) {
	p := NewSSEParser(r, maxEvent)
	var evs []refEvent
	for {
		ev, err := p.Next()
		if err != nil {
			d, ok := p.Retry()
			if !ok {
				d = -1
			}
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return evs, d, err
		}
		evs = append(evs, refEvent{string(ev.Type), string(ev.Data), string(ev.ID)})
	}
}

// readers returns r's content through several chunkings (results must not
// depend on read boundaries).
func readers(in string) map[string]io.Reader {
	return map[string]io.Reader{
		"whole":   strings.NewReader(in),
		"onebyte": iotest.OneByteReader(strings.NewReader(in)),
		"half":    iotest.HalfReader(strings.NewReader(in)),
	}
}

func TestSSEParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in string
		want     []refEvent
		retry    time.Duration
	}{
		{name: "basic", in: "data: hello\n\n", want: []refEvent{{"", "hello", ""}}, retry: -1},
		{name: "multiline", in: "data: a\ndata:b\ndata\n\n", want: []refEvent{{"", "a\nb\n", ""}}, retry: -1},
		{name: "crlf and cr", in: "data: a\r\n\r\ndata: b\r\rdata: c\n\n", want: []refEvent{{"", "a", ""}, {"", "b", ""}, {"", "c", ""}}, retry: -1},
		{name: "type id retry", in: "event: delta\nid: 7\nretry: 1500\ndata: {}\n\ndata: x\n\n", want: []refEvent{{"delta", "{}", "7"}, {"", "x", "7"}}, retry: 1500 * time.Millisecond},
		{name: "comments only", in: ": keepalive\n\n: ping\n\n", retry: -1},
		{name: "no data no dispatch", in: "event: x\n\ndata: y\n\n", want: []refEvent{{"", "y", ""}}, retry: -1},
		{name: "trailing incomplete", in: "data: a\n\ndata: b\n", want: []refEvent{{"", "a", ""}}, retry: -1},
		{name: "bom", in: bom + "data: a\n\n", want: []refEvent{{"", "a", ""}}, retry: -1},
		{name: "bom only at start", in: "\n" + bom + "data: a\n\n", retry: -1},
		{name: "one space stripped", in: "data:  a\n\n", want: []refEvent{{"", " a", ""}}, retry: -1},
		{name: "id with nul ignored", in: "id: 1\ndata: a\n\nid: 2\x00\ndata: b\n\n", want: []refEvent{{"", "a", "1"}, {"", "b", "1"}}, retry: -1},
		{name: "bad retry ignored", in: "retry: 1x\nretry: \ndata: a\n\n", want: []refEvent{{"", "a", ""}}, retry: -1},
		{name: "unknown field", in: "foo: bar\ndata: a\n\n", want: []refEvent{{"", "a", ""}}, retry: -1},
		{name: "openai done", in: "data: {\"x\":1}\n\ndata: [DONE]\n\n", want: []refEvent{{"", `{"x":1}`, ""}, {"", "[DONE]", ""}}, retry: -1},
	}
	for _, c := range cases {
		ref, rretry := refParse(c.in)
		if !slices.Equal(ref, c.want) || rretry != c.retry {
			t.Fatalf("%s: reference %v %v, want %v %v", c.name, ref, rretry, c.want, c.retry)
		}
		for rn, r := range readers(c.in) {
			got, retry, err := parseAll(r, 0)
			if err != nil || !slices.Equal(got, c.want) || retry != c.retry {
				t.Errorf("%s/%s: %v %v %v, want %v %v", c.name, rn, got, retry, err, c.want, c.retry)
			}
		}
	}
}

func TestSSELimits(t *testing.T) {
	t.Parallel()
	big := "data: " + strings.Repeat("x", 100) + "\n\n"
	if _, _, err := parseAll(strings.NewReader(big), 64); !errors.Is(err, ErrEventTooLarge) {
		t.Errorf("long line: %v, want ErrEventTooLarge", err)
	}
	many := strings.Repeat("data: "+strings.Repeat("y", 20)+"\n", 10) + "\n"
	if _, _, err := parseAll(strings.NewReader(many), 64); !errors.Is(err, ErrEventTooLarge) {
		t.Errorf("large event: %v, want ErrEventTooLarge", err)
	}
	// A line may grow the buffer beyond the initial size up to the limit.
	long := "data: " + strings.Repeat("z", 3*sseInitialBuf) + "\n\n"
	evs, _, err := parseAll(iotest.HalfReader(strings.NewReader(long)), 0)
	if err != nil || len(evs) != 1 || len(evs[0].data) != 3*sseInitialBuf {
		t.Errorf("long event: %d events, %v", len(evs), err)
	}
	boom := errors.New("boom")
	p := NewSSEParser(iotest.ErrReader(boom), 0)
	for range 2 { // sticky
		if _, err := p.Next(); !errors.Is(err, boom) {
			t.Errorf("read error: %v", err)
		}
	}
}

// loopReader replays a fixed stream forever without allocating.
type loopReader struct {
	b   []byte
	off int
}

func (l *loopReader) Read(p []byte) (int, error) {
	n := copy(p, l.b[l.off:])
	l.off = (l.off + n) % len(l.b)
	return n, nil
}

// sseSample is a representative openai-chat stream chunk (≈ 250 B/event).
const sseSample = ": keepalive\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hello, world"},"finish_reason":null}]}` + "\n\n" +
	"event: delta\r\nid: 42\r\n" +
	`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}` + "\r\n\r\n"

// TestSSEZeroAlloc (E-35): 0 allocs per event after warmup.
func TestSSEZeroAlloc(t *testing.T) {
	p := NewSSEParser(&loopReader{b: []byte(sseSample)}, 0)
	for range 100 { // warmup grows the buffers
		if _, err := p.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if n := testing.AllocsPerRun(1000, func() {
		if _, err := p.Next(); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Errorf("allocs per event = %v, want 0", n)
	}
}

func BenchmarkSSEParse(b *testing.B) {
	p := NewSSEParser(&loopReader{b: []byte(sseSample)}, 0)
	b.SetBytes(int64(len(sseSample) / 2))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := p.Next(); err != nil {
			b.Fatal(err)
		}
	}
}

// FuzzSSEParse: differential against refParse, independent of chunking,
// never panics, events always ≤ the limit.
func FuzzSSEParse(f *testing.F) {
	for _, s := range []string{sseSample, "data: a\r\n\r\n", bom + "retry: 10\n\n", "\r\r\n\n", "id: \x00\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		want, wretry := refParse(string(in))
		for rn, r := range readers(string(in)) {
			got, retry, err := parseAll(r, 0)
			if err != nil || !slices.Equal(got, want) || retry != wretry {
				t.Fatalf("%s: %q → %q %v %v, want %q %v", rn, in, got, retry, err, want, wretry)
			}
		}
		small, _, err := parseAll(bytes.NewReader(in), 16)
		if err != nil && !errors.Is(err, ErrEventTooLarge) {
			t.Fatalf("limit: %v", err)
		}
		for _, ev := range small {
			if len(ev.data) > 16 {
				t.Fatalf("event %q over limit", ev.data)
			}
		}
	})
}
