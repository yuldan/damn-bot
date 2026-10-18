package codetools

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

// TestSchemaTokens is CT-27 for the five eager file tools: description
// ≤ 160 chars and compact schema ≤ 800 bytes (≤ 200 o200k tokens at the
// ≥ 4 bytes/token of JSON schemas; the exact o200k count is in bench).
func TestSchemaTokens(t *testing.T) {
	t.Parallel()
	names := map[string]bool{}
	for _, raw := range Schemas() {
		var s struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		var c bytes.Buffer
		if err := json.Compact(&c, raw); err != nil {
			t.Fatal(err)
		}
		names[s.Name] = true
		if len([]rune(s.Description)) > 160 || c.Len() > 800 {
			t.Errorf("%s: description %d chars, schema %d bytes", s.Name, len([]rune(s.Description)), c.Len())
		}
	}
	if len(names) != 5 {
		t.Errorf("tools %v", names)
	}
}

// TestRunDispatch covers JSON dispatch: strict decoding, unknown tools.
func TestRunDispatch(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	ctx := context.Background()
	for name, args := range map[string]string{
		"write_file": `{"path":"a.txt","content":"x\n"}`, "read_file": `{"path":"a.txt"}`,
		"glob": `{"pattern":"*.txt"}`, "grep": `{"pattern":"x"}`,
	} {
		if _, err := v.e.Run(ctx, name, json.RawMessage(args), Opts{}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := v.e.Run(ctx, "edit_file", json.RawMessage(`{"path":"a.txt","tag":"00000000","edits":[{"bogus":1}]}`), Opts{})
	wantCode(t, err, CodeInvalidArgs)
	_, err = v.e.Run(ctx, "nope", json.RawMessage(`{}`), Opts{})
	wantCode(t, err, CodeInvalidArgs)
}
