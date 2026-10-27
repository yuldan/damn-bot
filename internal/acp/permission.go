package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/loop"
)

// Origin is the ACP approval surface (ADR-197); it must match loop.OriginACP
// so another surface's request can never be answered here.
const Origin = loop.OriginACP

// MaxPreview mirrors the approval preview cap (SPEC §18.2).
const MaxPreview = 3500

// PermissionAnswer is the client's decision.
type PermissionAnswer uint8

// Permission answers. Only AllowOnce/AllowAlways reach the dispatcher; a
// session grant is downgraded when the call forbids it.
const (
	PermissionAllowOnce PermissionAnswer = iota
	PermissionAllowAlways
	PermissionReject
)

func (a PermissionAnswer) String() string {
	switch a {
	case PermissionAllowOnce:
		return "allow_once"
	case PermissionAllowAlways:
		return "allow_always"
	case PermissionReject:
		return "reject_once"
	}
	return "reject_once"
}

// PermissionRequest is the protocol-neutral view of one approval. It carries
// the same Origin and Preview contract as tools/loop (ADR-197) without
// importing tools (E-03).
type PermissionRequest struct {
	SessionID    string
	Tool         string
	ToolCallID   string
	Class        string
	Effect       string
	Preview      string
	Origin       string
	Args         json.RawMessage
	AllowSession bool
}

// Permission errors.
var (
	ErrPreviewRequired = errors.New("acp: a dangerous action needs a preview")
	ErrOrigin          = errors.New("acp: approval origin is not acp")
	ErrNoPermitter     = errors.New("acp: session has no permission channel")
	ErrPermission      = errors.New("acp: invalid permission result")
)

// PreviewRequired mirrors tools.PreviewRequired using the classifier's closed
// labels: shell destructive and P26 classes, workspace writes, and any
// non-read effect whose class is not a shell label.
func PreviewRequired(effect, class string) bool {
	if effect == string(loop.EffectRead) {
		return false
	}
	if strings.HasPrefix(class, "shell:") {
		for _, label := range []string{"destructive", "irreversible", "monetary", "delegated_agent_bypass"} {
			if strings.Contains(class, label) {
				return true
			}
		}
		return false
	}
	return class == "write" || class == ""
}

// ClipPreview bounds a preview at a rune boundary with an explicit marker.
func ClipPreview(s string) string {
	if utf8.RuneCountInString(s) <= MaxPreview {
		return s
	}
	return string([]rune(s)[:MaxPreview]) + "\n… (preview truncated)"
}

// Validate checks the contract before the client is asked: acp origin, a
// tool, and a preview for every dangerous action.
func (r *PermissionRequest) Validate() error {
	if r.Origin != Origin {
		return ErrOrigin
	}
	if r.Tool == "" {
		return ErrPermission
	}
	if PreviewRequired(r.Effect, r.Class) && strings.TrimSpace(r.Preview) == "" {
		return ErrPreviewRequired
	}
	return nil
}

// Permitter asks the client. Implementations write session/request_permission
// and resolve the matching response; they own the request id lifecycle.
type Permitter interface {
	Ask(ctx context.Context, req PermissionRequest) (PermissionAnswer, error)
}

// PermissionOption is one option offered in session/request_permission.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionToolCall describes the action being approved.
type PermissionToolCall struct {
	ToolCallID string          `json:"toolCallId,omitempty"`
	Title      string          `json:"title,omitempty"`
	Kind       string          `json:"kind,omitempty"`
	RawInput   json.RawMessage `json:"rawInput,omitempty"`
}

// PermissionParams is the session/request_permission payload. It is our
// recorded contract; conformance to a real client is proved separately.
type PermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  PermissionToolCall `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
	Preview   string             `json:"preview,omitempty"`
}

// PermissionResult is the client's session/request_permission result.
type PermissionResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome is "selected" with an option id, or "cancelled".
type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// Options returns the offered options: allow_always only when the call allows
// a session grant; P26 classes never do (ADR-088).
func (r *PermissionRequest) Options() []PermissionOption {
	opts := []PermissionOption{
		{OptionID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		{OptionID: "reject_once", Name: "Reject", Kind: "reject_once"},
	}
	if r.AllowSession && !p26(r.Class) {
		opts = append(opts, PermissionOption{OptionID: "allow_always", Name: "Allow for this session", Kind: "allow_always"})
	}
	return opts
}

func p26(class string) bool {
	for _, label := range []string{"destructive", "irreversible", "monetary", "delegated_agent_bypass"} {
		if strings.Contains(class, label) {
			return true
		}
	}
	return false
}

// Params builds the wire payload (options + clipped preview).
func (r *PermissionRequest) Params() PermissionParams {
	return PermissionParams{
		SessionID: r.SessionID,
		ToolCall: PermissionToolCall{
			ToolCallID: r.ToolCallID, Title: r.Tool, Kind: r.Tool, RawInput: r.Args,
		},
		Options: r.Options(),
		Preview: ClipPreview(r.Preview),
	}
}

// DecodePermissionResult maps a client result to an answer. An unknown option
// or outcome is rejected, never treated as allow.
func DecodePermissionResult(raw json.RawMessage) (PermissionAnswer, error) {
	var res PermissionResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return PermissionReject, fmt.Errorf("%w: %w", ErrPermission, err)
	}
	if res.Outcome.Outcome == "cancelled" {
		return PermissionReject, nil
	}
	if res.Outcome.Outcome != "selected" {
		return PermissionReject, ErrPermission
	}
	switch res.Outcome.OptionID {
	case "allow_once":
		return PermissionAllowOnce, nil
	case "allow_always":
		return PermissionAllowAlways, nil
	case "reject_once":
		return PermissionReject, nil
	}
	return PermissionReject, ErrPermission
}

// Approver implements loop.Approver for one ACP server. It denies whenever the
// request is not from this surface, the session is in plan mode, a dangerous
// action lacks a preview, or the client has no permission channel.
type Approver struct {
	Sessions *Registry
	Timeout  time.Duration // 0 = no extra deadline
}

// Approve asks the session's client and maps the answer to the loop contract.
func (a *Approver) Approve(ctx context.Context, req *loop.ApprovalRequest) (loop.ApprovalResult, error) {
	deny := loop.ApprovalResult{Decision: loop.DecisionDeny}
	if a == nil || a.Sessions == nil || req == nil {
		return deny, nil
	}
	s, ok := a.Sessions.Get(req.SessionID)
	if !ok || s.PlanMode() {
		return deny, nil
	}
	perm := PermissionRequest{
		SessionID: req.SessionID, Tool: req.Tool, Effect: string(req.Effect),
		Preview: ClipPreview(req.Preview), Origin: req.Origin, Args: req.Args,
	}
	if err := perm.Validate(); err != nil {
		return deny, nil
	}
	p, ok := s.Permitter()
	if !ok {
		return deny, nil
	}
	askCtx := ctx
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		askCtx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	answer, err := p.Ask(askCtx, perm)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return deny, fmt.Errorf("acp: approve: %w", cerr)
		}
		return deny, nil
	}
	decision := loop.DecisionAllowOnce
	if answer == PermissionAllowAlways && perm.AllowSession {
		decision = loop.DecisionAllowSession
	}
	if answer == PermissionReject {
		decision = loop.DecisionDeny
	}
	return loop.ApprovalResult{Decision: decision}, nil
}
