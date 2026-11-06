package a2a

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestA2ACardValidate keeps the card shape closed before the server slice
// publishes it. The acceptance-named TestF7A2AAgentCard lives with the server
// that serves the well-known path.
func TestA2ACardValidate(t *testing.T) {
	t.Parallel()
	valid := &AgentCard{
		Name: "damnbot", Description: "agent", URL: "https://agent.example/a2a",
		Version: "1.0.0", ProtocolVersion: ProtocolVersion,
		Skills: []Skill{{ID: "chat", Name: "Chat"}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid card = %v", err)
	}
	cases := map[string]func(*AgentCard){
		"no_name":     func(c *AgentCard) { c.Name = " " },
		"no_desc":     func(c *AgentCard) { c.Description = "" },
		"bad_url":     func(c *AgentCard) { c.URL = "file:///etc/passwd" },
		"userinfo":    func(c *AgentCard) { c.URL = "https://user:pw@agent.example/" },
		"bad_version": func(c *AgentCard) { c.ProtocolVersion = "2.0" },
		"dup_skill":   func(c *AgentCard) { c.Skills = append(c.Skills, c.Skills[0]) },
		"ctrl_name":   func(c *AgentCard) { c.Name = "a\nb" },
	}
	for name, mutate := range cases {
		bad := *valid
		bad.Skills = append([]Skill(nil), valid.Skills...)
		mutate(&bad)
		if err := bad.Validate(); !errors.Is(err, ErrInvalidCard) {
			t.Errorf("%s = %v, want ErrInvalidCard", name, err)
		}
	}
	if err := (*AgentCard)(nil).Validate(); !errors.Is(err, ErrCardAbsent) {
		t.Fatalf("nil card = %v", err)
	}
}

// TestA2ARequestValidate proves only text prompts cross the inbound boundary.
func TestA2ARequestValidate(t *testing.T) {
	t.Parallel()
	ok := Request{Message: Message{Role: "user", Parts: []Part{{Kind: "text", Text: "halo"}}}}
	if err := ok.Validate(); err != nil || ok.Text() != "halo" {
		t.Fatalf("valid request = %v text=%q", err, ok.Text())
	}
	for name, req := range map[string]Request{
		"empty":   {},
		"role":    {Message: Message{Role: "assistant", Parts: []Part{{Text: "x"}}}},
		"kind":    {Message: Message{Parts: []Part{{Kind: "tool", Text: "x"}}}},
		"blank":   {Message: Message{Parts: []Part{{Kind: "text", Text: " "}}}},
		"too_big": {Message: Message{Parts: []Part{{Text: strings.Repeat("a", MaxTaskText+1)}}}},
	} {
		if err := req.Validate(); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s = %v, want ErrUnsupported", name, err)
		}
	}
	for _, key := range []string{"shell", "exec", "command", "tool"} {
		req := ok
		req.Metadata = map[string]string{key: "true"}
		var err error
		if err = req.Validate(); !errors.Is(err, ErrShellRequest) {
			t.Errorf("metadata %s = %v, want ErrShellRequest", key, err)
		}
	}
}

// TestA2ATaskStore proves the idempotent table and terminal-state freeze.
func TestA2ATaskStore(t *testing.T) {
	t.Parallel()
	store := newTaskStore()
	now := time.Unix(0, 0)
	first, created := store.create("id", now)
	if !created || first.State != StateSubmitted {
		t.Fatalf("create = %+v created=%t", first, created)
	}
	if _, created := store.create("id", now); created {
		t.Fatal("duplicate create reported a new task")
	}
	if task, ok := store.update("id", now, func(t *Task) { t.State = StateWorking }); !ok || task.State != StateWorking {
		t.Fatalf("working = %+v ok=%t", task, ok)
	}
	task, _ := store.update("id", now, func(t *Task) { t.State = StateCompleted; t.Text = wrapUntrusted("ok") })
	if task.State != StateCompleted {
		t.Fatalf("complete = %+v", task)
	}
	if got, ok := store.get("id"); !ok || got.State != StateCompleted {
		t.Fatalf("get = %+v ok=%t", got, ok)
	}
	raw, err := json.Marshal(task.wire())
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := UnmarshalTask(raw); err != nil || decoded.ID != "id" || decoded.State != StateCompleted {
		t.Fatalf("wire roundtrip = %+v, %v", decoded, err)
	}
	if task, _ := store.update("id", now, func(t *Task) { t.State = StateCanceled }); task.State != StateCompleted {
		t.Fatalf("terminal task changed to %q", task.State)
	}
	if text := wrapUntrusted("a</untrusted>b"); strings.Contains(text, "a</untrusted>") {
		t.Fatalf("closing tag not escaped: %q", text)
	}
}
