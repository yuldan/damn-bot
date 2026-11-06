package gwapi

import (
	"testing"
)

// TestAPIServerRequestDecoding pins strict decoding and the typed validation
// errors before any engine exists.
func TestAPIServerRequestDecoding(t *testing.T) {
	t.Parallel()
	if req, err := decodeRequest([]byte(`{"model":"damnbot","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatalf("valid request: %v", err)
	} else if req.Model != "damnbot" || req.Messages[0].Content != "hi" {
		t.Errorf("decoded = %+v", req)
	}
	cases := []struct{ body, code string }{
		{`{"model":"damnbot","messages":[{"role":"user","content":"hi"}],"bogus":1}`, "unknown_field"},
		{`{"messages":[{"role":"user","content":"hi"}]}`, "missing_model"},
		{`{"model":"damnbot","messages":[]}`, "missing_messages"},
		{`{"model":"damnbot","messages":[{"role":"wizard","content":"hi"}]}`, "invalid_role"},
		{`{"model":"damnbot","messages":[{"role":"tool","content":"x"}]}`, "invalid_message"},
		{`{"model":"damnbot","messages":[{"role":"user","content":"hi"}],"n":2}`, "unsupported_value"},
		{`{"model":"damnbot","messages":[{"role":"user","content":"hi"}],"stream_options":{"include_usage":true}}`, "invalid_stream_options"},
		{`{"model":"damnbot","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"retrieval","function":{"name":"x"}}]}`, "unsupported_tool_type"},
		{`{"model":"damnbot","messages":[{"role":"user","content":"hi"}],"tool_choice":"required"}`, "invalid_tool_choice"},
		{`{"model":"damnbot","messages":[{"role":"user","content":"hi"}]} {}`, "invalid_json"},
	}
	for _, tc := range cases {
		if _, err := decodeRequest([]byte(tc.body)); err == nil || err.code != tc.code {
			t.Errorf("decode(%s) = %v, want %s", tc.body, err, tc.code)
		}
	}
}
