package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		frame string
		code  int // 0 = accepted
	}{
		{"request", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, 0},
		{"string id", `{"jsonrpc":"2.0","id":"a","method":"x","params":{}}`, 0},
		{"null id", `{"jsonrpc":"2.0","id":null,"method":"x"}`, 0},
		{"notification", `{"jsonrpc":"2.0","method":"x"}`, 0},
		{"array params", `{"jsonrpc":"2.0","id":2,"method":"x","params":[1]}`, 0},
		{"bad json", `{"jsonrpc":`, CodeParseError},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"x"}]`, CodeInvalidRequest},
		{"wrong version", `{"jsonrpc":"1.0","id":1,"method":"x"}`, CodeInvalidRequest},
		{"no method", `{"jsonrpc":"2.0","id":1}`, CodeInvalidRequest},
		{"object id", `{"jsonrpc":"2.0","id":{},"method":"x"}`, CodeInvalidRequest},
		{"scalar params", `{"jsonrpc":"2.0","id":1,"method":"x","params":"sk-secret"}`, CodeInvalidParams},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			req, rpcErr := ParseRequest([]byte(c.frame))
			if c.code == 0 {
				if rpcErr != nil {
					t.Fatalf("ParseRequest = %v", rpcErr)
				}
				if req.Method == "" {
					t.Fatal("no method")
				}
				return
			}
			if rpcErr == nil || rpcErr.Code != c.code {
				t.Fatalf("code = %v, want %d", rpcErr, c.code)
			}
			if strings.Contains(rpcErr.Message, "sk-secret") {
				t.Fatalf("error echoes params: %q", rpcErr.Message)
			}
		})
	}
}

func TestErrorMessagesAreFixed(t *testing.T) {
	t.Parallel()
	for _, e := range []*Error{ErrParse, ErrRequest, ErrMethod, ErrParams, ErrInternal} {
		if e.Message == "" || strings.ContainsAny(e.Message, "{}[]\"") {
			t.Fatalf("unexpected message %q", e.Message)
		}
		if e.Code > 0 {
			t.Fatalf("code %d is not an error code", e.Code)
		}
	}
}

func TestEncodeResponse(t *testing.T) {
	t.Parallel()
	frame, err := EncodeResponse(&Response{ID: nil, Error: ErrRequest})
	if err != nil {
		t.Fatal(err)
	}
	if frame[len(frame)-1] != '\n' {
		t.Fatal("frame has no LF terminator")
	}
	var got map[string]any
	if uerr := json.Unmarshal(frame[:len(frame)-1], &got); uerr != nil {
		t.Fatal(uerr)
	}
	if got["jsonrpc"] != "2.0" {
		t.Fatalf("jsonrpc = %v", got["jsonrpc"])
	}
	if id, ok := got["id"]; !ok || id != nil {
		t.Fatalf("id = %v (present %v), want null", id, ok)
	}
	if _, ok := got["result"]; ok {
		t.Fatal("error response carries result")
	}

	nullResult, err := EncodeResponse(&Response{ID: json.RawMessage(`7`), Result: json.RawMessage(`null`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nullResult), `"result":null`) {
		t.Fatalf("null result frame = %s", nullResult)
	}
}
