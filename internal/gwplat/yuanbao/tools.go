package yuanbao

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool is one Yuanbao tool descriptor; the composition root adapts it to the
// internal/tools registry.
type Tool struct {
	Name        string
	Description string
	Effect      string
	Schema      json.RawMessage
	Invoke      func(ctx context.Context, args json.RawMessage) (string, error)
}

// Tool names of TOOL-049..053.
const (
	ToolGroupInfo     = "yb_query_group_info"
	ToolGroupMembers  = "yb_query_group_members"
	ToolSearchSticker = "yb_search_sticker"
	ToolSendDM        = "yb_send_dm"
	ToolSendSticker   = "yb_send_sticker"
)

// ToolNames returns the five names in stable order.
func ToolNames() []string {
	return []string{ToolGroupInfo, ToolGroupMembers, ToolSearchSticker, ToolSendDM, ToolSendSticker}
}

// Tools returns the adapter's tool descriptors. Sends are non_idempotent so
// the dispatcher's approval policy applies.
func (a *Adapter) Tools() []Tool {
	return []Tool{
		{
			Name: ToolGroupInfo, Effect: "read",
			Description: "Return the cached Yuanbao group name and member count",
			Schema:      json.RawMessage(`{"type":"object","properties":{"group":{"type":"string","maxLength":128}},"required":["group"],"additionalProperties":false}`),
			Invoke:      a.invokeGroupInfo,
		},
		{
			Name: ToolGroupMembers, Effect: "read",
			Description: "List the cached members of one Yuanbao group",
			Schema:      json.RawMessage(`{"type":"object","properties":{"group":{"type":"string","maxLength":128}},"required":["group"],"additionalProperties":false}`),
			Invoke:      a.invokeGroupMembers,
		},
		{
			Name: ToolSearchSticker, Effect: "read",
			Description: "Search the operator's allowed Yuanbao sticker catalogue",
			Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":128}},"additionalProperties":false}`),
			Invoke:      a.invokeSearchSticker,
		},
		{
			Name: ToolSendDM, Effect: "non_idempotent",
			Description: "Send one direct message to an allowlisted Yuanbao identity",
			Schema:      json.RawMessage(`{"type":"object","properties":{"user":{"type":"string","maxLength":128},"text":{"type":"string","maxLength":4096}},"required":["user","text"],"additionalProperties":false}`),
			Invoke:      a.invokeSendDM,
		},
		{
			Name: ToolSendSticker, Effect: "non_idempotent",
			Description: "Send one catalogued sticker to a Yuanbao chat",
			Schema:      json.RawMessage(`{"type":"object","properties":{"chat":{"type":"string","maxLength":128},"sticker_id":{"type":"string","maxLength":128}},"required":["chat","sticker_id"],"additionalProperties":false}`),
			Invoke:      a.invokeSendSticker,
		},
	}
}

// Tool returns one descriptor by name.
func (a *Adapter) Tool(name string) (Tool, bool) {
	for _, tool := range a.Tools() {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

func decodeToolArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: arguments", ErrBadFrame)
	}
	return nil
}

func (a *Adapter) invokeGroupInfo(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Group string `json:"group"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	g, err := a.GroupInfo(args.Group)
	if err != nil {
		return "", err
	}
	return encodeToolResult(g)
}

func (a *Adapter) invokeGroupMembers(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Group string `json:"group"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	members, err := a.GroupMembers(args.Group)
	if err != nil {
		return "", err
	}
	return encodeToolResult(members)
}

func (a *Adapter) invokeSearchSticker(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	return encodeToolResult(a.SearchSticker(args.Query))
}

func (a *Adapter) invokeSendDM(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		User string `json:"user"`
		Text string `json:"text"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	if args.User == "" || args.Text == "" {
		return "", fmt.Errorf("%w: user and text are required", ErrBadFrame)
	}
	if err := a.SendDM(ctx, args.User, args.Text); err != nil {
		return "", err
	}
	return "sent", nil
}

func (a *Adapter) invokeSendSticker(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Chat      string `json:"chat"`
		StickerID string `json:"sticker_id"`
	}
	if err := decodeToolArgs(raw, &args); err != nil {
		return "", err
	}
	if args.Chat == "" || args.StickerID == "" {
		return "", fmt.Errorf("%w: chat and sticker_id are required", ErrBadFrame)
	}
	if err := a.SendSticker(ctx, args.Chat, args.StickerID); err != nil {
		return "", err
	}
	return "sent", nil
}

func encodeToolResult(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("yuanbao: encode result: %w", err)
	}
	return string(raw), nil
}
