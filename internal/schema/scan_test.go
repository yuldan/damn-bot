package schema

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

type validateCase struct {
	name string
	doc  string
	want string // exact model-facing message; "" = valid
}

// validJSON are strict-JSON documents covering every token form.
func validJSON() []string {
	return []string{
		`0`, `-0`, `-1.5e+10`, `1E-2`, `123456789012345678901234567890`, `1e400`, `true`, `false`, `null`,
		`""`, `"\"\\\/\b\f\n\r\t"`, `"\u0000\u00e9\uD83D\uDE00"`, `"é😀"`, " \t\r\n[ ] ", `{}`, `[[],{},[{"a":[]}]]`,
		`{"a":1,"b":{"a":2},"c":[{"a":3},{"a":3}]}`, `{"\u00e9":1,"é2":2}`, strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth),
	}
}

// TestCheck: the strict tokenizer accepts every valid form and reports each
// syntax error with a stable message and byte offset (model-facing, E-68).
func TestCheck(t *testing.T) {
	t.Parallel()
	for _, d := range validJSON() {
		if e := check([]byte(d)); e != nil || !json.Valid([]byte(d)) {
			t.Errorf("check(%q) = %v (json.Valid %v), want nil", d, e, json.Valid([]byte(d)))
		}
	}
	for _, c := range syntaxCases() {
		e := check([]byte(c.doc))
		var err error = e
		if e == nil || e.Error() != c.want || !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: check(%q) = %v\nwant %s", c.name, c.doc, e, c.want)
		}
	}
}

// TestJSONNoDuplicateKeys (SPEC §4.4): duplicate member names are rejected
// wherever they occur, also when equal only after unescaping and in objects
// large enough to switch detection to a set; equal names in different
// objects are fine.
func TestJSONNoDuplicateKeys(t *testing.T) {
	t.Parallel()
	for _, d := range duplicateDocs() {
		if e := check([]byte(d)); e == nil || e.Kind != KindSyntax {
			t.Errorf("check(%.60q) = %v, want duplicate-key syntax error", d, e)
		}
	}
	if e := check([]byte(`{"a":{"a":1},"b":[{"a":1},{"a":1}]}`)); e != nil {
		t.Errorf("same name in sibling/nested objects rejected: %v", e)
	}
}

func duplicateDocs() []string {
	var big strings.Builder
	big.WriteString(`{"pattern":"x","free":{`)
	for i := range 40 {
		big.WriteString(`"k` + strings.Repeat("x", i) + `":0,`)
	}
	big.WriteString(`"kxxx":1}}`)
	return []string{
		`{"pattern":"a","pattern":"a"}`,
		`{"pattern":"a","\u0070attern":"a"}`,
		`{"pattern":"a","free":{"a":{"b":1,"b":1}}}`,
		`{"pattern":"a","free":[{"x":1,"y":2,"x":3}]}`,
		`{"pattern":"a","meta":{"é":"1","\u00e9":"2"}}`,
		`{"pattern":"a","items":[{"id":"1","id":"1","text":"t","status":"done"}]}`,
		big.String(),
	}
}

// TestStringTokens: decoded content and code-point counts of strings.
func TestStringTokens(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`"plain"`:                     "plain",
		`"a\"b\\c\/d"`:                `a"b\c/d`,
		`"\b\f\n\r\t"`:                "\b\f\n\r\t",
		`"\u0041\u00e9\u20ac"`:        "Aé€",
		`"\ud83d\ude00x\uD834\uDD1E"`: "😀x𝄞",
		`"mixé😀\u0000"`:               "mixé😀\x00",
	}
	for raw, want := range cases {
		s := scanner{b: []byte(raw)}
		st, ok := s.readString()
		if !ok {
			t.Fatalf("readString(%s): %v", raw, s.err)
		}
		if got := string(unescape(nil, st.raw)); got != want || st.esc != strings.Contains(raw, `\`) {
			t.Errorf("unescape(%s) = %q (esc %v), want %q", raw, got, st.esc, want)
		}
		if st.runes != utf8.RuneCountInString(want) {
			t.Errorf("runes(%s) = %d, want %d", raw, st.runes, utf8.RuneCountInString(want))
		}
	}
}

// FuzzJSONCheck: the strict tokenizer never accepts what encoding/json
// rejects, and decodes strings exactly like encoding/json.
func FuzzJSONCheck(f *testing.F) {
	for _, d := range validJSON() {
		f.Add([]byte(d))
	}
	for _, c := range syntaxCases() {
		f.Add([]byte(c.doc))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		e := check(doc)
		if e != nil && (e.Offset < 0 || e.Offset > len(doc) || e.Kind == KindViolation) {
			t.Fatalf("malformed error %#v", e)
		}
		if e == nil && !json.Valid(doc) {
			t.Fatalf("check accepted %q, encoding/json rejects", doc)
		}
		s := scanner{b: doc}
		if s.ws() != '"' {
			return
		}
		st, ok := s.readString()
		var want string
		if ok && json.Unmarshal(doc[:s.i], &want) == nil && string(unescape(nil, st.raw)) != want {
			t.Fatalf("unescape(%q) = %q, encoding/json %q", doc[:s.i], unescape(nil, st.raw), want)
		}
	})
}

func syntaxCases() []validateCase {
	return []validateCase{
		{"empty", ``, "invalid JSON at byte 0: unexpected end of input"},
		{"spaces only", `   `, "invalid JSON at byte 3: unexpected end of input"},
		{"unclosed object", `{`, "invalid JSON at byte 1: expected object key"},
		{"trailing comma", `{"pattern":"a",}`, "invalid JSON at byte 15: expected object key"},
		{"single quotes", `{"pattern":'a'}`, "invalid JSON at byte 11: expected value"},
		{"bare key", `{pattern:"a"}`, "invalid JSON at byte 1: expected object key"},
		{"trailing value", `{"pattern":"a"} {}`, "invalid JSON at byte 16: unexpected data after top-level value"},
		{"missing colon", `{"pattern" "a"}`, "invalid JSON at byte 11: expected ':' after object key"},
		{"missing comma", `{"pattern":"a" "path":"b"}`, "invalid JSON at byte 15: expected ',' or '}'"},
		{"control char", "{\"pattern\":\"a\x01\"}", "invalid JSON at byte 13: control character in string"},
		{"invalid escape", `{"pattern":"\q"}`, "invalid JSON at byte 12: invalid escape"},
		{"short unicode escape", `{"pattern":"\u12"}`, "invalid JSON at byte 12: invalid \\u escape"},
		{"lone high surrogate", `{"pattern":"\ud800"}`, "invalid JSON at byte 18: lone UTF-16 surrogate escape"},
		{"lone low surrogate", `{"pattern":"\udc00x"}`, "invalid JSON at byte 18: lone UTF-16 surrogate escape"},
		{"invalid UTF-8", "{\"pattern\":\"\xff\"}", "invalid JSON at byte 12: invalid UTF-8 in string"},
		{"unterminated string", `{"pattern":"abc`, "invalid JSON at byte 15: unterminated string"},
		{"leading zero", `{"pattern":"x","context":01}`, "invalid JSON at byte 26: expected ',' or '}'"},
		{"bare fraction dot", `{"pattern":"x","context":1.}`, "invalid JSON at byte 27: invalid number"},
		{"bare minus", `{"pattern":"x","context":-}`, "invalid JSON at byte 26: expected value"},
		{"leading dot", `{"pattern":"x","context":.5}`, "invalid JSON at byte 25: expected value"},
		{"empty exponent", `{"pattern":"x","context":1e}`, "invalid JSON at byte 27: invalid number"},
		{"NaN", `{"pattern":"x","ratio":NaN}`, "invalid JSON at byte 23: expected value"},
		{"Infinity", `{"pattern":"x","ratio":Infinity}`, "invalid JSON at byte 23: expected value"},
		{"truncated literal", `{"pattern":"x","flag":tru}`, "invalid JSON at byte 22: invalid literal"},
		{"capitalised literal", `{"pattern":"x","flag":True}`, "invalid JSON at byte 22: expected value"},
		{"duplicate key", `{"pattern":"a","pattern":"b"}`, `invalid JSON at byte 15: duplicate object key "pattern"`},
		{"duplicate key via escape", `{"pattern":"a","p\u0061ttern":"b"}`, `invalid JSON at byte 15: duplicate object key "pattern"`},
		{"duplicate key free-form", `{"pattern":"a","free":{"k":1,"k":2}}`, `invalid JSON at byte 29: duplicate object key "k"`},
		{"duplicate unknown key", `{"pattern":"a","meta":{"x":"1","x":"2"}}`, `invalid JSON at byte 31: duplicate object key "x"`},
		{"too deep", `{"pattern":"x","free":` + strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth) + `}`, "invalid JSON at byte 85: nesting deeper than 64"},
	}
}
