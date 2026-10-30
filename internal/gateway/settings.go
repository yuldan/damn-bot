package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ChatSettings is the per-chat configuration seam.
type ChatSettings interface {
	ChatSetting(ctx context.Context, platform, chatID, key string) (value string, found bool, err error)
	SetChatSetting(ctx context.Context, platform, chatID, key, value string, now int64) error
	HomeChannel(ctx context.Context) (platform, chatID, threadID string, found bool, err error)
	SetHomeChannel(ctx context.Context, platform, chatID, threadID string, now int64) error
}

// HomeName renders platform:chat/thread.
func HomeName(platform, chatID, threadID string) string {
	if threadID != "" {
		return platform + ":" + chatID + "/" + threadID
	}
	return platform + ":" + chatID
}

// toggle implements /timestamps and /delay: a per-chat boolean stored in the
// same chat settings table as /footer. /delay gates the existing human pacing.
func (c *Commands) toggle(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.settings == nil {
		return unsupported(cmd.Name)
	}
	key := cmd.Name
	if key == "delay" {
		key = "human_delay"
	}
	return c.chatToggle(ctx, t, cmd, key)
}

func (c *Commands) chatToggle(ctx context.Context, t *Ticket, cmd Command, key string) CommandResult {
	arg := "status"
	if len(cmd.Args) > 0 {
		arg = strings.ToLower(cmd.Args[0])
	}
	if len(cmd.Args) > 1 || (arg != "on" && arg != "off" && arg != "status") {
		return usage(cmd.Name + " [on|off|status]")
	}
	if arg == "status" {
		on, err := c.chatFlag(ctx, t, key)
		if err != nil {
			return CommandResult{Handled: true, Reply: cmd.Name + " setting unavailable"}
		}
		if on {
			return CommandResult{Handled: true, Reply: cmd.Name + " on"}
		}
		return CommandResult{Handled: true, Reply: cmd.Name + " off (default)"}
	}
	if err := c.settings.SetChatSetting(ctx, t.Scope.Platform, t.Scope.ChatID, key, strconv.FormatBool(arg == "on"), c.now().UnixMilli()); err != nil {
		return CommandResult{Handled: true, Reply: "could not change " + cmd.Name}
	}
	return CommandResult{Handled: true, Reply: cmd.Name + " " + arg}
}

func (c *Commands) chatFlag(ctx context.Context, t *Ticket, key string) (bool, error) {
	if c.settings == nil {
		return false, nil
	}
	raw, found, err := c.settings.ChatSetting(ctx, t.Scope.Platform, t.Scope.ChatID, key)
	if err != nil {
		return false, fmt.Errorf("gateway: chat setting %s: %w", key, err)
	}
	if !found {
		return false, nil
	}
	value, perr := strconv.ParseBool(strings.TrimSpace(raw))
	if perr != nil {
		return false, fmt.Errorf("gateway: chat setting %s: %w", key, perr)
	}
	return value, nil
}
