package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// PinState is the closed pin state of one tool (GAP-SEC-04, ADR-272).
type PinState string

// Pin states. Only [PinPinned] may be dispatched.
const (
	PinPinned      PinState = "pinned"
	PinChanged     PinState = "changed"
	PinQuarantined PinState = "quarantined"
)

var (
	// ErrPinsUnavailable is a client without a pin store.
	ErrPinsUnavailable = errors.New("mcp: pin store unavailable")
	// ErrNotPinned is a review of a tool that was never listed.
	ErrNotPinned = errors.New("mcp: tool is not pinned")
	// ErrPinStale is a review whose observed definition changed meanwhile.
	ErrPinStale = errors.New("mcp: pin changed during review")
)

// Pin is one tool pin. Diff is a JSON summary without the raw schema.
type Pin struct {
	Server       string
	Tool         string
	SchemaSHA256 string
	FirstSeen    int64
	ApprovedAt   int64
	ApprovedBy   string
	State        PinState
	Diff         string
}

// Blocked reports whether the tool is quarantined from dispatch.
func (p *Pin) Blocked() bool { return p.State != PinPinned }

// PinStore is the persistence seam of tool pins. The composition root backs
// it with internal/ctxdb/mcpstore; internal/mcp never imports ctxdb (E-06).
// ApprovePin is conditional: it succeeds only while the stored digest is
// still digest, so a review can never approve a definition it did not see.
type PinStore interface {
	Pin(ctx context.Context, server, tool string) (Pin, bool, error)
	Pins(ctx context.Context, server string) ([]Pin, error)
	PutPin(ctx context.Context, p Pin) error
	ApprovePin(ctx context.Context, server, tool, digest, by string, at int64) (bool, error)
}

// pinDefinition is the canonical bytes a pin commits to. The input schema is
// already normalized by NormalizeSchema; annotations are canonicalized too so
// a whitespace-only rewrite never trips the rug-pull gate.
type pinDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// toolDigest is sha256 of the canonical definition (SEC-04).
func toolDigest(t *Tool) (string, error) {
	schema, err := canonicalJSON(t.InputSchema)
	if err != nil {
		return "", fmt.Errorf("%w: %s schema: %w", ErrInvalidSchema, t.Name, err)
	}
	annotations, err := canonicalJSON(t.Annotations)
	if err != nil {
		return "", fmt.Errorf("%w: %s annotations: %w", ErrInvalidSchema, t.Name, err)
	}
	raw, err := json.Marshal(pinDefinition{
		Name: t.Name, Description: t.Description, Schema: schema, Annotations: annotations,
	})
	if err != nil {
		return "", fmt.Errorf("mcp: pin digest: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON re-encodes raw with sorted object keys. Empty input stays nil.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("canonical json: %w", err)
	}
	return json.Marshal(value) //nolint:wrapcheck // caller wraps with the tool name
}

type pinDiff struct {
	Change         string `json:"change"`
	ApprovedSHA256 string `json:"approved_sha256,omitempty"`
	ObservedSHA256 string `json:"observed_sha256,omitempty"`
	DescriptionLen int    `json:"description_len"`
	SchemaLen      int    `json:"schema_len"`
	AnnotationsLen int    `json:"annotations_len,omitempty"`
}

func diffSummary(change, approved, observed string, t *Tool) string {
	raw, err := json.Marshal(pinDiff{
		Change: change, ApprovedSHA256: approved, ObservedSHA256: observed,
		DescriptionLen: len(t.Description), SchemaLen: len(t.InputSchema), AnnotationsLen: len(t.Annotations),
	})
	if err != nil {
		return `{"change":"` + change + `"}`
	}
	return string(raw)
}

// pinTools evaluates a listing. The first complete listing pins every tool;
// afterwards a changed digest, new tool or removed tool is quarantined. Only
// pinned tools are returned, so a quarantined tool never reaches the catalog.
func (c *Client) pinTools(ctx context.Context, tools []Tool) ([]Tool, error) { //nolint:gocognit,gocyclo // one closed state machine per listing
	if c.pins == nil {
		return nil, ErrPinsUnavailable
	}
	c.pinMu.Lock()
	defer c.pinMu.Unlock()
	existing, err := c.pins.Pins(ctx, c.server.Name)
	if err != nil {
		return nil, fmt.Errorf("mcp: read pins: %w", err)
	}
	prior := make(map[string]Pin, len(existing))
	for _, p := range existing {
		prior[p.Tool] = p
	}
	first := len(existing) == 0
	now := c.nowMillis()
	seen := make(map[string]bool, len(tools))
	visible := make([]Tool, 0, len(tools))
	for i := range tools {
		tool := tools[i]
		digest, derr := toolDigest(&tool)
		if derr != nil {
			return nil, derr
		}
		seen[tool.Name] = true
		p, known := prior[tool.Name]
		switch {
		case !known && first:
			p = Pin{
				Server: c.server.Name, Tool: tool.Name, SchemaSHA256: digest,
				FirstSeen: now, ApprovedAt: now, ApprovedBy: "first-seen", State: PinPinned,
			}
			p.Diff = diffSummary("initial", "", digest, &tool)
			if err := c.pins.PutPin(ctx, p); err != nil {
				return nil, fmt.Errorf("mcp: pin %s: %w", tool.Name, err)
			}
			visible = append(visible, tool)
		case !known:
			p = Pin{
				Server: c.server.Name, Tool: tool.Name, SchemaSHA256: digest,
				FirstSeen: now, State: PinChanged,
			}
			p.Diff = diffSummary("added", "", digest, &tool)
			if err := c.pins.PutPin(ctx, p); err != nil {
				return nil, fmt.Errorf("mcp: quarantine new tool %s: %w", tool.Name, err)
			}
		case p.SchemaSHA256 == digest:
			if !p.Blocked() {
				visible = append(visible, tool)
			}
		default:
			approved := p.SchemaSHA256
			p.SchemaSHA256 = digest
			p.State = PinChanged
			p.Diff = diffSummary("modified", approved, digest, &tool)
			if err := c.pins.PutPin(ctx, p); err != nil {
				return nil, fmt.Errorf("mcp: quarantine %s: %w", tool.Name, err)
			}
		}
	}
	if first {
		return visible, nil
	}
	for _, p := range existing {
		if seen[p.Tool] || p.State == PinQuarantined {
			continue
		}
		p.State = PinQuarantined
		p.Diff = diffSummary("removed", p.SchemaSHA256, "", &Tool{Name: p.Tool})
		if err := c.pins.PutPin(ctx, p); err != nil {
			return nil, fmt.Errorf("mcp: quarantine removed tool %s: %w", p.Tool, err)
		}
	}
	return visible, nil
}

func (c *Client) pinGate(ctx context.Context, name string) string {
	if c.pins == nil {
		return "mcp_pins_unavailable"
	}
	p, found, err := c.pins.Pin(ctx, c.server.Name, name)
	switch {
	case err != nil:
		return "mcp_pin_error"
	case !found:
		return "mcp_tool_unpinned"
	case p.Blocked():
		return "mcp_tool_changed"
	}
	return ""
}

// Pins returns every pin of this server for the review surface.
func (c *Client) Pins(ctx context.Context) ([]Pin, error) {
	if c.pins == nil {
		return nil, ErrPinsUnavailable
	}
	pins, err := c.pins.Pins(ctx, c.server.Name)
	if err != nil {
		return nil, fmt.Errorf("mcp: read pins: %w", err)
	}
	return pins, nil
}

// Review approves the currently observed definition of one quarantined tool.
// The consent itself goes through the host approval seam (default deny); the
// store update is conditional on the digest the approver saw, so a server
// cannot race a second change into the approval. F5-11 exposes this through
// `damnbot mcp review`.
func (c *Client) Review(ctx context.Context, tool, approvedBy string) error {
	if c.pins == nil {
		return ErrPinsUnavailable
	}
	if tool == "" || len(tool) > 128 || approvedBy == "" || len(approvedBy) > host.MaxApprovalField {
		return fmt.Errorf("%w: tool or approver", ErrInvalidConfig)
	}
	p, found, err := c.pins.Pin(ctx, c.server.Name, tool)
	if err != nil {
		return fmt.Errorf("mcp: read pin: %w", err)
	}
	if !found {
		return fmt.Errorf("%w: %s", ErrNotPinned, tool)
	}
	if !p.Blocked() {
		return nil
	}
	answer, err := Approve(ctx, c.host, &host.ApprovalRequest{
		Tool: tool, Effect: host.EffectNonIdempotent, Class: "mcp_review",
		Preview: p.Diff, Args: json.RawMessage(`{}`),
	})
	if err != nil {
		return err
	}
	if !answer.Allowed() {
		return ErrDenied
	}
	ok, err := c.pins.ApprovePin(ctx, c.server.Name, tool, p.SchemaSHA256, approvedBy, c.nowMillis())
	if err != nil {
		return fmt.Errorf("mcp: approve pin: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrPinStale, tool)
	}
	return nil
}

// nowMillis uses the host clock when wired and falls back to the wall clock
// for the non-security audit timestamp. Expiry decisions never call this.
func (c *Client) nowMillis() int64 {
	if now, err := host.CapabilitiesOf(c.host).Clock.Now(); err == nil {
		return now.UnixMilli()
	}
	return time.Now().UnixMilli()
}
