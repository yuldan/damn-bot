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
