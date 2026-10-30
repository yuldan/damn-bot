package gateway

import "strings"

// Caps is the outbound capability descriptor an adapter declares (SPEC §9.7).
// The outq scheduler derives its token buckets from Rate; the adapter enforces
// MaxChunksPerReply by sending the remainder as a markdown attachment.
type Caps struct {
	Rate              RateCaps
	MaxChunksPerReply int
	MaxLen            int
}

// RateCaps are the three outbound buckets of GAP-GW-03: one per chat, one per
// group chat, and one global bucket per platform.
type RateCaps struct {
	PerChat  Rate
	PerGroup Rate
	Global   Rate
}

// DefaultMaxChunksPerReply is the GAP-GW-03 cap: a reply longer than this many
// chunks is completed by one `.md` attachment.
const DefaultMaxChunksPerReply = 5

// TelegramCaps is the official Bot API budget (SPEC §9.7): 1 message/s per
// chat, 20/minute per group, 30/s globally.
func TelegramCaps() Caps {
	return Caps{
		Rate: RateCaps{
			PerChat:  Rate{PerSecond: 1, Burst: 1},
			PerGroup: Rate{PerSecond: 20.0 / 60.0, Burst: 1},
			Global:   Rate{PerSecond: 30, Burst: 30},
		},
		MaxChunksPerReply: DefaultMaxChunksPerReply,
	}
}

// GroupChat reports whether platform/chat is a group conversation. Telegram
// group and supergroup ids are negative; the outq only applies the per-group
// bucket to those chats.
func GroupChat(platform, chat string) bool {
	return platform == "telegram" && strings.HasPrefix(chat, "-")
}
