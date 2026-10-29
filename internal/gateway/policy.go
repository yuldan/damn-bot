package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ChatPolicyStore reads the raw per-chat override of GAP-GW-05 from the
// temporary module_state row until the SPEC config key lands with its ADR.
type ChatPolicyStore interface {
	ChatOverride(ctx context.Context, platform, chatID string) (value string, found bool, err error)
}

// Triggers reports whether in fires a turn: DMs always run; groups need a
// mention, reply to the bot, or command unless waived; bots always need a
// mention (EC-PLAT-022).
func (in *Inbound) Triggers(requireMention bool) bool {
	if in.SenderIsBot {
		return in.Mentioned
	}
	if !in.Group {
		return true
	}
	if in.Mentioned || in.ReplyToBot || in.Command {
		return true
	}
	return !requireMention
}

// needsOverride reports whether the per-chat override can change the decision:
// a group message from a human that does not already trigger a mention, a
// reply, or a command.
func (g *Gate) needsOverride(in *Inbound) bool {
	return in.Group && !in.SenderIsBot && g.chats != nil &&
		!in.Mentioned && !in.ReplyToBot && !in.Command
}

// override resolves the per-chat require_mention value, defaulting to true
// when the chat has no override. A malformed value is an error (fail-closed).
func (g *Gate) override(ctx context.Context, in *Inbound) (bool, error) {
	raw, found, err := g.chats.ChatOverride(ctx, in.Platform, in.ChatID)
	if err != nil {
		return false, fmt.Errorf("gateway: chat policy %s/%s: %w", in.Platform, in.ChatID, err)
	}
	if !found {
		return true, nil
	}
	value, perr := strconv.ParseBool(strings.TrimSpace(raw))
	if perr != nil {
		return false, fmt.Errorf("gateway: chat policy %s/%s: %w", in.Platform, in.ChatID, perr)
	}
	return value, nil
}

// mentionReason is the pre-turn gate of GAP-GW-05.
func (g *Gate) mentionReason(ctx context.Context, in *Inbound) (Reason, error) {
	require := true
	if g.needsOverride(in) {
		value, err := g.override(ctx, in)
		if err != nil {
			return ReasonNone, err
		}
		require = value
	}
	if in.Triggers(require) {
		return ReasonNone, nil
	}
	return ReasonMentionRequired, nil
}
