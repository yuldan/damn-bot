//go:build !minimal

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// DispatcherInvoker is the execute_code RPC seam: a Starlark tool() call is
// dispatched through the same pipeline as a model tool call (approval, taint,
// sandbox, journal). D must be set by the composition root after the
// dispatcher exists; a nil dispatcher fails closed.
type DispatcherInvoker struct{ D *Dispatcher }

// Tool implements code.ToolInvoker.
func (i *DispatcherInvoker) Tool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if i == nil || i.D == nil {
		return nil, errors.New("execute_code: tool pipeline is not wired")
	}
	out := i.D.Dispatch(ctx, Call{CallID: "rpc", Name: name, Args: args})
	if out.Err != nil {
		return nil, fmt.Errorf("%s: %s", out.Err.Code, out.Err.Message)
	}
	body, err := json.Marshal(map[string]any{"content": out.Content, "meta": out.Meta})
	if err != nil {
		return nil, fmt.Errorf("execute_code: encode tool result: %w", err)
	}
	return body, nil
}
