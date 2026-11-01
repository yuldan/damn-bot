package provider

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestSurrogateEdgeCases covers the EC-FIO surrogate rows: a lone UTF-16
// surrogate can arrive from a provider delta or a model-generated string and
// must never reach the wire or the transcript as invalid UTF-8.
func TestSurrogateEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(*testing.T)
	}{
		{"EC-FIO-005", ecFio005},
		{"EC-FIO-009", ecFio009},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// EC-FIO-005: a lone surrogate in a stream delta becomes U+FFFD (the
// encoding/json contract); a model string carrying the WTF-8 form of a
// surrogate is encoded as U+FFFD instead of failing the send.
func ecFio005(t *testing.T) {
	var dd DeltaDecoder
	got, err := dd.Decode([]byte(`{"choices":[{"index":0,"delta":{"content":"a\ud800b"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Content) != "a\uFFFDb" || !utf8.Valid(got.Content) {
		t.Fatalf("lone surrogate decoded as %q", got.Content)
	}
	low, err := dd.Decode([]byte(`{"choices":[{"index":0,"delta":{"content":"a\udc00b"}}]}`))
	if err != nil || string(low.Content) != "a\uFFFDb" {
		t.Fatalf("lone low surrogate decoded as %q (%v)", low.Content, err)
	}

	msg := Message{Role: "assistant", Content: "x\xed\xa0\x80y"} // WTF-8 of U+D800
	raw, _, _, err := encodeMessage(&Route{Provider: "p", Model: "m"}, &msg, &Turn{Role: "assistant", Content: msg.Content}, false, "")
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}
	if !utf8.Valid(raw) || !bytes.Contains(raw, []byte(`\ufffd`)) {
		t.Fatalf("encoded message = %q", raw)
	}
	var back struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Content != "x"+strings.Repeat("\uFFFD", 3)+"y" {
		t.Fatalf("round-trip content = %q", back.Content)
	}
}

// EC-FIO-009: writing JSON never fails or emits invalid UTF-8 for a string
// that holds an encoded surrogate; both encoders replace it with U+FFFD.
func ecFio009(t *testing.T) {
	raw := quote([]byte("a\xed\xa0\x80b"))
	if raw == nil || !utf8.Valid(raw) {
		t.Fatalf("quote = %q", raw)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s != "a"+strings.Repeat("\uFFFD", 3)+"b" {
		t.Fatalf("quoted string = %q", s)
	}
	body, err := marshal(map[string]string{"content": "a\xed\xa0\x80b"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !utf8.Valid(body) || strings.Contains(string(body), "\\ud800") {
		t.Fatalf("body = %q", body)
	}
}
