package assemble

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestPrefixStableWithinTurn: iteration n+1's message sequence begins with
// iteration n's bytes; the system message and the frozen volatile tier never
// move (ADR-067 butir 2).
func TestPrefixStableWithinTurn(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	turn := a.Freeze(fixtureVolatile())
	view := SessionView{
		Phase:   PhaseF1,
		Context: renderContext(t, a, fixtureContext()),
		Tools:   fixtureTools(),
		Turn:    turn,
		History: []Message{{Role: "user", Content: "Fix the failing test."}},
	}
	first, _, err := a.Build(context.Background(), &view)
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	secondView := view
	secondView.History = append(append([]Message{}, view.History...),
		Message{Role: "assistant", Content: "calling shell"},
		Message{Role: "tool", Content: "ok <runtime>2/40</runtime>"},
	)
	second, _, err := a.Build(context.Background(), &secondView)
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	if !reflect.DeepEqual(first.Messages[0], second.Messages[0]) {
		t.Error("system message changed between iterations")
	}
	one, err := marshalMessages(first.Messages)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	two, err := marshalMessages(second.Messages)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	prefix := bytes.TrimSuffix(one, []byte("]"))
	if !bytes.HasPrefix(two, append(prefix, ',')) {
		t.Errorf("iteration 2 does not extend iteration 1:\n%s\n----\n%s", one, two)
	}
}

func TestBuildWithoutTurn(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	req, est, err := a.Build(context.Background(), &SessionView{
		Phase:   PhaseF1,
		History: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(req.Messages) != 2 || req.Messages[1].Content != "hello" {
		t.Errorf("messages = %+v", req.Messages)
	}
	if est.Total == 0 {
		t.Error("estimate total is zero")
	}
}

func TestBuildRequiresUserMessageForTurn(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	_, _, err := a.Build(context.Background(), &SessionView{
		Phase:   PhaseF1,
		Turn:    a.Freeze(&VolatileInput{}),
		History: []Message{{Role: "assistant", Content: "no user"}},
	})
	if !errors.Is(err, ErrNoUserMessage) {
		t.Fatalf("err = %v, want ErrNoUserMessage", err)
	}
}

func TestBuildRequiresDeps(t *testing.T) {
	t.Parallel()
	if _, err := New(Deps{}); !errors.Is(err, ErrDeps) {
		t.Fatalf("New(Deps{}) = %v, want ErrDeps", err)
	}
}

// TestBuildScrubsVolatile: notice text and names pass through the redactor.
func TestBuildScrubsVolatile(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, func(s string) string { return strings.ReplaceAll(s, "sk-live-secret", "[REDACTED:secret]") })
	turn := a.Freeze(&VolatileInput{Notices: []string{"auth failed for sk-live-secret"}})
	req, _, err := a.Build(context.Background(), &SessionView{
		Phase:   PhaseF1,
		Turn:    turn,
		History: []Message{{Role: "user", Content: "go"}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	body, err := MarshalRequest(req)
	if err != nil {
		t.Fatalf("MarshalRequest: %v", err)
	}
	if bytes.Contains(body, []byte("sk-live-secret")) {
		t.Errorf("volatile secret leaked: %s", body)
	}
}

func TestMarshalRequestWireShape(t *testing.T) {
	t.Parallel()
	data, err := MarshalRequest(Request{
		Messages: []Message{{Role: "system", Content: "<untrusted>"}},
		Tools:    []Tool{{Name: "x", Description: "d", Parameters: json.RawMessage(`{}`)}},
	})
	if err != nil {
		t.Fatalf("MarshalRequest: %v", err)
	}
	if !bytes.Contains(data, []byte(`"messages"`)) || !bytes.Contains(data, []byte(`"tools"`)) {
		t.Errorf("wire shape = %s", data)
	}
	if bytes.Contains(data, []byte(`\u003c`)) {
		t.Errorf("HTML escaping found: %s", data)
	}
	var decoded struct {
		Messages []Message `json:"messages"`
		Tools    []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Messages) != 1 || len(decoded.Tools) != 1 || decoded.Tools[0].Type != "function" {
		t.Errorf("decoded = %+v", decoded)
	}
}
