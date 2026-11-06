package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Tool is one A2A tool descriptor. The composition root adapts it to
// internal/tools; writing the schema here keeps the package free of that
// dependency.
type Tool struct {
	Name        string
	Description string
	Effect      string
	Schema      json.RawMessage
	Invoke      func(ctx context.Context, args json.RawMessage) (string, error)
}

// Tool names of TOOL-001..005.
const (
	ToolCall        = "a2a_call"
	ToolDiscover    = "a2a_discover"
	ToolHistory     = "a2a_history"
	ToolList        = "a2a_list"
	ToolOrchestrate = "a2a_orchestrate"
)

// ToolNames returns the five names in stable order.
func ToolNames() []string {
	return []string{ToolCall, ToolDiscover, ToolHistory, ToolList, ToolOrchestrate}
}

// Tools returns the client's tool descriptors. None of them ever returns the
// bearer token; a2a_call and a2a_orchestrate are approval-eligible effects.
func (c *Client) Tools() []Tool {
	return []Tool{
		{
			Name: ToolCall, Effect: "non_idempotent",
			Description: "Send one task to a configured A2A agent and return its untrusted result",
			Schema:      json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string","maxLength":128},"task":{"type":"string","maxLength":32768},"task_id":{"type":"string","maxLength":128}},"required":["agent","task"],"additionalProperties":false}`),
			Invoke:      c.invokeCall,
		},
		{
			Name: ToolDiscover, Effect: "read",
			Description: "Fetch and validate the agent card of a configured A2A agent",
			Schema:      json.RawMessage(`{"type":"object","properties":{"agent":{"type":"string","maxLength":128}},"additionalProperties":false}`),
			Invoke:      c.invokeDiscover,
		},
		{
			Name: ToolHistory, Effect: "read",
			Description: "Return this session's bounded A2A interaction history",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Invoke:      c.invokeHistory,
		},
		{
			Name: ToolList, Effect: "read",
			Description: "List the configured A2A agents and their URLs (never a secret)",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Invoke:      c.invokeList,
		},
		{
			Name: ToolOrchestrate, Effect: "non_idempotent",
			Description: "Fan one task out to a bounded set of A2A agents and collect their results",
			Schema:      json.RawMessage(`{"type":"object","properties":{"task":{"type":"string","maxLength":32768},"agents":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":4},"depth":{"type":"integer","minimum":1,"maximum":2}},"required":["task","agents"],"additionalProperties":false}`),
			Invoke:      c.invokeOrchestrate,
		},
	}
}

// callArgs is the a2a_call payload.
type callArgs struct {
	Agent  string `json:"agent"`
	Task   string `json:"task"`
	TaskID string `json:"task_id"`
}

// orchestrateArgs is the a2a_orchestrate payload.
type orchestrateArgs struct {
	Task   string   `json:"task"`
	Agents []string `json:"agents"`
	Depth  int      `json:"depth"`
}

func decodeArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: arguments", ErrUnsupported)
	}
	return nil
}

func (c *Client) invokeCall(ctx context.Context, raw json.RawMessage) (string, error) {
	var args callArgs
	if err := decodeArgs(raw, &args); err != nil {
		return "", err
	}
	if args.Agent == "" || args.Task == "" {
		return "", fmt.Errorf("%w: agent and task are required", ErrUnsupported)
	}
	req := Request{TaskID: args.TaskID, Message: Message{Role: "user", Parts: []Part{{Kind: "text", Text: args.Task}}}}
	task, err := c.Send(ctx, args.Agent, req)
	if err != nil {
		return "", err
	}
	if !task.State.Terminal() {
		if task, err = c.Wait(ctx, args.Agent, task.ID, 30*time.Second); err != nil {
			return "", err
		}
	}
	return renderTask(&task)
}

func renderTask(task *Task) (string, error) {
	if task.State == StateFailed {
		return "", fmt.Errorf("a2a: task %s failed: %s", task.ID, task.Error)
	}
	if task.Text != "" {
		return task.Text, nil
	}
	return string(task.State), nil
}

type cardEntry struct {
	Agent string    `json:"agent"`
	Card  AgentCard `json:"card"`
}

func (c *Client) invokeDiscover(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Agent string `json:"agent"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return "", err
	}
	if args.Agent != "" {
		card, err := c.Discover(ctx, args.Agent)
		if err != nil {
			return "", err
		}
		return marshalResult(cardEntry{Agent: args.Agent, Card: card})
	}
	out := make([]cardEntry, 0, len(c.peers))
	for _, p := range c.Peers() {
		card, err := c.Discover(ctx, p.Name)
		if err != nil {
			return "", err
		}
		out = append(out, cardEntry{Agent: p.Name, Card: card})
	}
	return marshalResult(out)
}

func (c *Client) invokeHistory(_ context.Context, raw json.RawMessage) (string, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return "", err
	}
	hist := c.History()
	if len(hist) == 0 {
		return "[]", nil
	}
	return marshalResult(hist)
}

func (c *Client) invokeList(_ context.Context, raw json.RawMessage) (string, error) {
	if err := decodeArgs(raw, &struct{}{}); err != nil {
		return "", err
	}
	peers := c.Peers()
	if len(peers) == 0 {
		return "[]", nil
	}
	return marshalResult(peers)
}

type orchestrateResult struct {
	Agent  string `json:"agent"`
	TaskID string `json:"task_id,omitempty"`
	State  State  `json:"state,omitempty"`
	Text   string `json:"text,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (c *Client) invokeOrchestrate(ctx context.Context, raw json.RawMessage) (string, error) {
	var args orchestrateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return "", err
	}
	if args.Task == "" || len(args.Agents) == 0 {
		return "", fmt.Errorf("%w: task and agents are required", ErrUnsupported)
	}
	if args.Depth > c.MaxDepth() {
		return "", fmt.Errorf("%w: %d > %d", ErrDepth, args.Depth, c.MaxDepth())
	}
	if len(args.Agents) > c.MaxFanOut() {
		return "", fmt.Errorf("%w: %d agents > %d", ErrUnsupported, len(args.Agents), c.MaxFanOut())
	}
	results := make([]orchestrateResult, len(args.Agents))
	var wg sync.WaitGroup
	for i, agent := range args.Agents {
		wg.Go(func() {
			results[i] = c.orchestrateOne(ctx, agent, args.Task)
		})
	}
	wg.Wait()
	return marshalResult(results)
}

func (c *Client) orchestrateOne(ctx context.Context, agent, text string) orchestrateResult {
	res := orchestrateResult{Agent: agent}
	task, err := c.Send(ctx, agent, Request{Message: Message{Role: "user", Parts: []Part{{Kind: "text", Text: text}}}})
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.TaskID = task.ID
	if !task.State.Terminal() {
		if task, err = c.Wait(ctx, agent, task.ID, 30*time.Second); err != nil {
			res.Error = err.Error()
			return res
		}
	}
	res.State, res.Text = task.State, task.Text
	if task.State == StateFailed {
		res.Error = task.Error
	}
	return res
}

func marshalResult(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("a2a: encode result: %w", err)
	}
	return string(raw), nil
}

// Tool returns one descriptor by name.
func (c *Client) Tool(name string) (Tool, bool) {
	for _, tool := range c.Tools() {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}
