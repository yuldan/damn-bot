package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/tools"
	"github.com/damnbot/damnbot/internal/toolset"
)

// runTools builds the declared tool list. allowed nil keeps every registered
// tool (no configured toolset); otherwise only members are sent to the model.
func runTools(reg *tools.Registry, allowed map[string]bool) ([]assemble.Tool, error) {
	out := make([]assemble.Tool, 0, len(reg.Schemas()))
	for _, raw := range reg.Schemas() {
		var doc struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("tool schema: %w", err)
		}
		if allowed != nil && !allowed[doc.Name] {
			continue
		}
		out = append(out, assemble.Tool{Name: doc.Name, Description: doc.Description, Parameters: doc.Parameters})
	}
	return out, nil
}

// registryNames is the availability set handed to toolset.Resolve.
func registryNames(reg *tools.Registry) map[string]bool {
	names := reg.Names()
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

// withDeferredMeta keeps tool_search/tool_invoke exposed when they are
// registered: they are infrastructure for the deferred catalog, not toolset
// members, and a toolset selection must not disconnect module tools.
func withDeferredMeta(sel *toolset.Selection, available map[string]bool) {
	if sel.Allowed == nil {
		return
	}
	for _, name := range []string{tools.ToolSearchName, tools.ToolInvokeName} {
		if !available[name] || sel.Allowed[name] {
			continue
		}
		sel.Allowed[name] = true
		sel.Tools = append(sel.Tools, name)
	}
	slices.Sort(sel.Tools)
}

type toolsetAdapter struct {
	disp    *tools.Dispatcher
	allowed map[string]bool // nil = every registered tool
}

func (t *toolsetAdapter) Prepare(_ context.Context, c loop.ToolCall) (loop.Prepared, error) {
	if t.allowed != nil && !t.allowed[c.Name] {
		return loop.Prepared{}, &loop.ToolError{
			Code: "unknown_tool", Message: "tool not in the active toolset: " + c.Name,
			Hint: "call one of the tools the active toolsets expose",
		}
	}
	if t.allowed != nil && c.Name == tools.ToolInvokeName {
		var inner struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(c.Arguments), &inner); err != nil {
			return loop.Prepared{}, &loop.ToolError{Code: loop.CodeInvalidArgs, Message: "tool_invoke arguments", Hint: err.Error()}
		}
		if inner.Name != "" && !t.allowed[inner.Name] {
			return loop.Prepared{}, &loop.ToolError{
				Code: "unknown_tool", Message: "tool not in the active toolset: " + inner.Name,
				Hint: "call one of the tools the active toolsets expose",
			}
		}
	}
	effect := t.disp.Effect(c.Name, json.RawMessage(c.Arguments))
	return loop.Prepared{
		Call: c, Effect: loop.EffectRead, Approval: loop.ApprovalNone,
		SideEffect: effect != tools.EffectRead,
	}, nil
}

func (t *toolsetAdapter) Dispatch(ctx context.Context, p *loop.Prepared) (loop.ToolResult, error) {
	out := t.disp.Dispatch(ctx, tools.Call{TurnID: p.Call.TurnID, CallID: p.Call.ID, Name: p.Call.Name, Args: json.RawMessage(p.Call.Arguments)})
	res := loop.ToolResult{Content: out.Render(), Tainted: out.Tainted}
	if out.Spilled != nil {
		res.BlobSHA = out.Spilled.SHA
	}
	if out.Denied {
		res.Err = &loop.ToolError{Code: loop.CodeDenied, Message: "denied"}
	}
	if out.Err != nil {
		res.Err = &loop.ToolError{Code: string(out.Err.Code), Message: out.Err.Message, Hint: out.Err.Hint}
	}
	return res, nil
}
