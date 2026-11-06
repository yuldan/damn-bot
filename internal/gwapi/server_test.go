package gwapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestAPIServerAuthAndLimits covers the mandatory bearer gate, healthz, and
// the model listing of the server envelope.
func TestAPIServerAuthAndLimits(t *testing.T) {
	t.Parallel()
	h := newTestServer(t, &fakeEngine{}, nil, testConfig()).Handler()
	if rec := get(t, h, "/healthz", nil); rec.Code != http.StatusOK {
		t.Errorf("healthz = %d", rec.Code)
	}
	rec := get(t, h, "/v1/models", nil)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("models without auth = %d", rec.Code)
	}
	rec = get(t, h, "/v1/models", map[string]string{"Authorization": "Bearer wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("models wrong auth = %d", rec.Code)
	}
	rec = get(t, h, "/v1/models", authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("models = %d", rec.Code)
	}
	var list ModelList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if list.Object != "list" || len(list.Data) != 1 || list.Data[0].ID != "damnbot" {
		t.Errorf("list = %+v", list)
	}
}

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
