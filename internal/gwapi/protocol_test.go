package gwapi

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

// TestF7PLAT028APIServer is the ledger test for the api_server surface.
func TestF7PLAT028APIServer(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{}
	h := newTestServer(t, eng, nil, testConfig()).Handler()
	checkChatEnvelope(t, h, eng)
	checkModels(t, h)
	checkHealthz(t, h)
}

func checkChatEnvelope(t *testing.T, h http.Handler, eng *fakeEngine) {
	t.Helper()
	rec := postJSON(t, h, chatterBody(""), authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	checkEnvelope(t, &resp)
	checkChoice(t, resp.Choices)
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if eng.count() != 1 {
		t.Fatalf("engine runs = %d, want 1", eng.count())
	}
	turn := eng.last()
	if turn.Model != "damnbot" || len(turn.Messages) != 1 || turn.Meta["origin"] != "api_server" || turn.Meta["tainted"] != "true" {
		t.Errorf("turn = %+v", turn)
	}
}

func checkEnvelope(t *testing.T, resp *ChatResponse) {
	t.Helper()
	if resp.ID != "chatcmpl-test" || resp.Object != "chat.completion" || resp.Model != "damnbot" || resp.Created != 1700000000 {
		t.Errorf("envelope = %+v", resp)
	}
}

func checkChoice(t *testing.T, choices []Choice) {
	t.Helper()
	if len(choices) != 1 || choices[0].Message == nil {
		t.Fatalf("choices = %+v", choices)
	}
	if choices[0].Message.Content != "hello" {
		t.Errorf("content = %q", choices[0].Message.Content)
	}
	if choices[0].FinishReason == nil || *choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %v", choices[0].FinishReason)
	}
}

func checkModels(t *testing.T, h http.Handler) {
	t.Helper()
	rec := get(t, h, "/v1/models", authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d", rec.Code)
	}
	var list ModelList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if list.Object != "list" || len(list.Data) != 1 || list.Data[0].ID != "damnbot" {
		t.Errorf("models = %+v", list)
	}
}

func checkHealthz(t *testing.T, h http.Handler) {
	t.Helper()
	rec := get(t, h, "/healthz", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Errorf("healthz = %d %s", rec.Code, rec.Body.String())
	}
}

// TestAPIServerProtocolConformance pins golden JSON/SSE and typed 400s.
func TestAPIServerProtocolConformance(t *testing.T) {
	t.Parallel()
	eng := &fakeEngine{}
	h := newTestServer(t, eng, nil, testConfig()).Handler()
	t.Run("nonstream golden", func(t *testing.T) { checkGolden(t, h, chatterBody(""), "conformance_nonstream.json") })
	t.Run("stream golden", func(t *testing.T) { checkGolden(t, h, chatterBody(`"stream":true`), "conformance_stream.sse") })
	t.Run("rejected requests", func(t *testing.T) { checkRejectedRequests(t, h) })
	t.Run("unknown field never reaches engine", func(t *testing.T) {
		before := eng.count()
		postJSON(t, h, chatterBody(`"bogus":1`), authHeaders())
		if eng.count() != before {
			t.Fatal("engine ran for a rejected request")
		}
	})
}

func checkGolden(t *testing.T, h http.Handler, body, name string) {
	t.Helper()
	rec := postJSON(t, h, body, authHeaders())
	if want := readGolden(t, name); rec.Body.String() != want {
		t.Errorf("body mismatch\n got: %q\nwant: %q", rec.Body.String(), want)
	}
	if name == "conformance_stream.sse" && rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
	}
}

func checkRejectedRequests(t *testing.T, h http.Handler) {
	t.Helper()
	cases := []struct{ name, body, code string }{
		{"unknown field", chatterBody(`"bogus":1`), "unknown_field"},
		{"missing model", `{"messages":[{"role":"user","content":"hi"}]}`, "missing_model"},
		{"missing messages", `{"model":"damnbot","messages":[]}`, "missing_messages"},
		{"bad role", `{"model":"damnbot","messages":[{"role":"wizard","content":"hi"}]}`, "invalid_role"},
		{"tool without id", `{"model":"damnbot","messages":[{"role":"tool","content":"x"}]}`, "invalid_message"},
		{"n > 1", chatterBody(`"n":2`), "unsupported_value"},
		{"stream options", chatterBody(`"stream_options":{"include_usage":true}`), "invalid_stream_options"},
		{"bad tool type", chatterBody(`"tools":[{"type":"retrieval","function":{"name":"x"}}]`), "unsupported_tool_type"},
		{"required without tools", chatterBody(`"tool_choice":"required"`), "invalid_tool_choice"},
		{"trailing data", chatterBody("") + `{}`, "invalid_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postJSON(t, h, tc.body, authHeaders())
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
			if er := decodeError(t, rec); er.Error.Type != TypeInvalidRequest || er.Error.Code != tc.code {
				t.Errorf("error = %+v, want %q", er.Error, tc.code)
			}
		})
	}
}

func readGolden(t *testing.T, name string) string {
	t.Helper()
	var (
		b   []byte
		err error
	)
	switch name {
	case "conformance_nonstream.json":
		b, err = os.ReadFile("testdata/conformance_nonstream.json")
	case "conformance_stream.sse":
		b, err = os.ReadFile("testdata/conformance_stream.sse")
	default:
		t.Fatalf("unknown golden %q", name)
	}
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return string(b)
}
