// Package a2a implements PLAT-001: the Agent-to-Agent v1.0 surface. The
// inbound server publishes an agent card at /.well-known/agent.json and
// answers task/send, task/get, task/cancel and task/stream over JSON-RPC;
// task ids are idempotent and task content is never executed as a shell. The
// client and its TOOL-001..005 descriptors live in tools.go; every outbound
// request goes through the injected netx HTTP client, never a package-level
// one.
package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/damnbot/damnbot/internal/gwplat"
)

// Wire constants of the A2A v1.0 surface.
const (
	ProtocolVersion = "1.0"
	CardPath        = "/.well-known/agent.json"
	RPCPath         = "/"

	// MaxCardBytes bounds the agent card the server publishes and the client
	// accepts.
	MaxCardBytes = 64 << 10
	// MaxTaskText bounds one task result.
	MaxTaskText = 32 << 10
)

// Errors of the A2A surface. Compared with errors.Is.
var (
	ErrConfig      = errors.New("a2a: invalid config")
	ErrInvalidCard = errors.New("a2a: invalid agent card")
	// ErrCardAbsent is module_unavailable{a2a_card_absent}: no card is
	// configured or the peer publishes none.
	ErrCardAbsent     = gwplat.Unavailablef("a2a", "a2a_card_absent")
	ErrAuth           = errors.New("a2a: unauthorized")
	ErrTaskNotFound   = errors.New("a2a: task not found")
	ErrUnsupported    = errors.New("a2a: unsupported task part")
	ErrShellRequest   = errors.New("a2a: task content requests execution")
	ErrPeerURL        = errors.New("a2a: peer URL is not a public http(s) endpoint")
	ErrDepth          = errors.New("a2a: orchestration depth exceeded")
	ErrNoPeer         = errors.New("a2a: unknown peer")
	ErrNoExecutor     = errors.New("a2a: task executor is required")
	ErrBadResponse    = errors.New("a2a: invalid JSON-RPC response")
	ErrStreamRequired = errors.New("a2a: peer does not stream")
)

// Capabilities declares the optional protocol features of one agent.
type Capabilities struct {
	Streaming bool `json:"streaming"`
}

// Skill is one advertised capability of an agent.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// AgentCard is the document served at CardPath.
type AgentCard struct {
	Name            string       `json:"name"`
	Description     string       `json:"description"`
	URL             string       `json:"url"`
	Version         string       `json:"version"`
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    Capabilities `json:"capabilities"`
	Skills          []Skill      `json:"skills,omitempty"`
}

// Validate rejects a card that no v1.0 peer can trust. The card is data, so
// validation never dials and never resolves a name.
func (c *AgentCard) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: card", ErrCardAbsent)
	}
	if err := c.validateText(); err != nil {
		return err
	}
	if !strings.HasPrefix(c.ProtocolVersion, "1.") {
		return fmt.Errorf("%w: protocolVersion %q", ErrInvalidCard, c.ProtocolVersion)
	}
	if err := validateCardURL(c.URL); err != nil {
		return err
	}
	return c.validateSkills()
}

func (c *AgentCard) validateText() error {
	for field, value := range map[string]string{
		"name": c.Name, "description": c.Description, "version": c.Version,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 1024 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: %s", ErrInvalidCard, field)
		}
	}
	if len(c.URL) > 1024 || strings.ContainsAny(c.URL, "\x00\r\n") {
		return fmt.Errorf("%w: url", ErrInvalidCard)
	}
	return nil
}

func validateCardURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return fmt.Errorf("%w: url", ErrInvalidCard)
	}
	return nil
}

func (c *AgentCard) validateSkills() error {
	seen := make(map[string]bool, len(c.Skills))
	for i := range c.Skills {
		s := &c.Skills[i]
		if s.ID == "" || s.Name == "" || seen[s.ID] {
			return fmt.Errorf("%w: skill %d", ErrInvalidCard, i)
		}
		seen[s.ID] = true
	}
	return nil
}

// RPCError is one JSON-RPC error returned by a peer.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Error implements error.
func (e *RPCError) Error() string { return fmt.Sprintf("a2a: rpc %d: %s", e.Code, e.Message) }

// Unwrap maps the closed task errors onto their sentinels.
func (e *RPCError) Unwrap() error {
	switch e.Code {
	case codeTaskNotFound:
		return ErrTaskNotFound
	case codeNoExecutor:
		return ErrNoExecutor
	default:
		return nil
	}
}

// Part is one content part of a task message. Only text parts are accepted:
// a tool/exec part would turn the inbound server into a remote executor.
type Part struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

// Message is the user message of one task request.
type Message struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

// Request is the task/send payload. TaskID is the idempotency key: resending
// the same id returns the existing task and never runs the executor twice.
type Request struct {
	TaskID   string            `json:"taskId,omitempty"`
	Message  Message           `json:"message"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Text returns the concatenated text parts.
func (r *Request) Text() string {
	var b strings.Builder
	for _, p := range r.Message.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// Validate rejects malformed requests and any request that asks the peer to
// execute a tool or a shell command. A2A carries prompts, not commands.
func (r *Request) Validate() error {
	if r == nil || len(r.Message.Parts) == 0 {
		return fmt.Errorf("%w: empty message", ErrUnsupported)
	}
	if r.Message.Role != "" && r.Message.Role != "user" {
		return fmt.Errorf("%w: role %q", ErrUnsupported, r.Message.Role)
	}
	text := 0
	for i := range r.Message.Parts {
		p := &r.Message.Parts[i]
		kind := p.Kind
		if kind == "" {
			kind = "text"
		}
		if kind != "text" {
			return fmt.Errorf("%w: kind %q", ErrUnsupported, kind)
		}
		if strings.TrimSpace(p.Text) == "" {
			return fmt.Errorf("%w: blank text part", ErrUnsupported)
		}
		text += len(p.Text)
	}
	if text > MaxTaskText {
		return fmt.Errorf("%w: task text %d bytes", ErrUnsupported, text)
	}
	for key := range r.Metadata {
		switch strings.ToLower(key) {
		case "shell", "exec", "execute", "command", "tool", "tools":
			return fmt.Errorf("%w: metadata %q", ErrShellRequest, key)
		}
	}
	return nil
}

// taskWire is the JSON shape of one task in responses.
type taskWire struct {
	ID        string `json:"id"`
	State     State  `json:"state"`
	Text      string `json:"text,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// UnmarshalTask decodes a task document; it is exported for the client and
// the tools.
func UnmarshalTask(raw json.RawMessage) (Task, error) {
	var w taskWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Task{}, fmt.Errorf("%w: task: %w", ErrBadResponse, err)
	}
	t, err := taskFromWire(&w)
	if err != nil {
		return Task{}, err
	}
	return t, nil
}
