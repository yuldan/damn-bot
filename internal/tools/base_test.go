package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/redact"
)

// testRedactor is the minimal redactor every dispatcher test needs: the
// constructor requires one (REVIEW-F1-SEC7 TL7-3).
func testRedactor() *redact.Redactor { return redact.New(redact.Options{}) }

func TestToolErrorAndCodes(t *testing.T) {
	t.Parallel()
	e := toolErr(CodeInvalidArgs, "fix it", "bad %d", 7)
	if e.Code != CodeInvalidArgs || e.Message != "bad 7" || e.Hint != "fix it" {
		t.Fatalf("toolErr = %+v", e)
	}
	if !strings.Contains(e.Error(), "invalid_args") {
		t.Fatalf("Error() = %q", e.Error())
	}
	wrapped := &ToolError{Code: CodeDenied, Message: "no", Err: errors.New("cause")}
	if codeOf(wrapped) != CodeDenied || codeOf(errors.New("x")) != CodeInternal || codeOf(nil) != "" {
		t.Fatal("codeOf classification")
	}
	if !errors.Is(wrapped, wrapped.Err) {
		t.Fatal("Unwrap")
	}
	if !strings.Contains(wrapped.Error(), "cause") {
		t.Fatal("cause missing")
	}
	if len(CodeSet()) != 38 {
		t.Fatalf("CodeSet = %d", len(CodeSet()))
	}
}

func TestArgsHashDeterministic(t *testing.T) {
	t.Parallel()
	a := argsHash(json.RawMessage(`{"body":"x"}`))
	b := argsHash(json.RawMessage(`{"body":"x"}`))
	c := argsHash(json.RawMessage(`{"body":"y"}`))
	if a != b || a == c {
		t.Fatal("argsHash not deterministic")
	}
}

func TestPreviewWrite(t *testing.T) {
	t.Parallel()
	got := previewWrite("a.txt", "one\ntwo\n")
	if !strings.Contains(got, "write a.txt (8 bytes)") || !strings.Contains(got, "+ one") || !strings.Contains(got, "+ two") {
		t.Fatalf("preview = %q", got)
	}
	long := previewWrite("a.txt", strings.Repeat("x", 200))
	if !strings.Contains(long, "…") {
		t.Fatalf("long line not clipped: %q", long)
	}
	many := previewWrite("a.txt", strings.Repeat("l\n", 30))
	if !strings.Contains(many, "more lines") {
		t.Fatalf("line bound missing: %q", many)
	}
}

func TestPreviewArgs(t *testing.T) {
	t.Parallel()
	if got := previewArgs("write_file", json.RawMessage(`{"path":"a","content":"hi"}`)); !strings.Contains(got, "+ hi") {
		t.Fatalf("write preview = %q", got)
	}
	edit := `{"path":"a","tag":"00000000","edits":[{"old_string":"x","new_string":"y"}]}`
	if got := previewArgs("edit_file", json.RawMessage(edit)); !strings.Contains(got, "- x") || !strings.Contains(got, "+ y") {
		t.Fatalf("edit preview = %q", got)
	}
	if got := previewArgs("edit_file", json.RawMessage(`{"path":"a","edits":[{"start_line":1,"end_line":2,"new_text":"z"}]}`)); !strings.Contains(got, "(line range)") {
		t.Fatalf("line preview = %q", got)
	}
	if previewArgs("read_file", json.RawMessage(`{}`)) != "" {
		t.Fatal("unexpected preview")
	}
	if previewArgs("write_file", json.RawMessage(`{`)) != "" {
		t.Fatal("invalid JSON preview")
	}
}

func TestClipLineUTF8(t *testing.T) {
	t.Parallel()
	if got := clipLine("short", 10); got != "short" {
		t.Fatalf("clip = %q", got)
	}
	got := clipLine(strings.Repeat("é", 100), 9)
	if !strings.HasSuffix(got, "…") || len(got) > 12 {
		t.Fatalf("clip = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatal("newline not folded")
	}
}

func TestEchoSchema(t *testing.T) {
	t.Parallel()
	if !strings.Contains(echoSchema("z"), `"name":"z"`) {
		t.Fatal("echoSchema")
	}
}

func TestToolSubject(t *testing.T) {
	t.Parallel()
	if got := toolSubject("write_file", "/a"); got != "write_file\x00/a" {
		t.Fatalf("subject = %q", got)
	}
}

func TestDefaultPolicy(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeAsk, ModeAutoEdit, ModeAuto, ModeHeadless, ModeHeadlessAuto} {
		if got := defaultPolicy(EffectRead, PolicyContext{Mode: mode}); got.Requirement != RequireAuto {
			t.Fatalf("read in %v = %v", mode, got.Requirement)
		}
	}
	cases := []struct {
		mode Mode
		want Requirement
	}{
		{ModeAsk, RequireAsk},
		{ModeAutoEdit, RequireAuto},
		{ModeAuto, RequireAuto},
		{ModeHeadless, RequireDeny},
		{ModeHeadlessAuto, RequireAuto},
	}
	for _, c := range cases {
		if got := defaultPolicy(EffectIdempotent, PolicyContext{Mode: c.mode}); got.Requirement != c.want {
			t.Fatalf("%v = %v, want %v", c.mode, got.Requirement, c.want)
		}
	}
	if !ModeHeadless.Headless() || !ModeHeadlessAuto.Headless() || ModeAsk.Headless() {
		t.Fatal("Headless()")
	}
}

func TestValidateNullAndErrors(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "x", schema: `{"name":"x","description":"x","parameters":{"type":"object","properties":{"path":{"type":"string"},"n":{"type":"integer"}},"required":["path"],"additionalProperties":false}}`}
	r := mustReg(t, tool)
	e, _ := r.Lookup("x")
	if err := e.Validate(json.RawMessage(`{"path":"a","n":null}`)); err != nil {
		t.Fatalf("null optional: %v", err)
	}
	if err := e.Validate(json.RawMessage(`{"path":null}`)); codeOf(err) != CodeInvalidArgs {
		t.Fatalf("required null: %v", err)
	}
	if err := e.Validate(json.RawMessage(`{"path":"a","extra":1}`)); !strings.Contains(err.Error(), "extra") {
		t.Fatalf("unknown field: %v", err)
	}
	if err := e.Validate(json.RawMessage(`{"path":"a"} tail`)); !strings.Contains(err.Error(), "byte") {
		t.Fatalf("syntax: %v", err)
	}
	if err := e.Validate(json.RawMessage(`{"path":1}`)); !strings.Contains(err.Error(), "path") {
		t.Fatalf("type: %v", err)
	}
}
