package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

const (
	chatFooter     = "footer"
	chatTimestamps = "timestamps"
	chatHumanDelay = "human_delay"
	humanDelayMin  = 300 * time.Millisecond
	humanDelayMax  = 1500 * time.Millisecond
)

type replyMeta struct {
	Iterations int
	CostUSD    float64
	CostKnown  bool
}

func (m *replyMeta) observe(u *event.Usage) {
	if u == nil {
		return
	}
	if u.Iterations > m.Iterations {
		m.Iterations = u.Iterations
	}
	if u.CostUSD != nil {
		m.CostUSD, m.CostKnown = *u.CostUSD, true
	}
}

type chatFlags struct {
	footer, timestamps, humanDelay bool
}

func (g *serveGateway) replyFlags(ctx context.Context, platform, chatID string) chatFlags {
	return chatFlags{
		footer:     g.chatFlag(ctx, platform, chatID, chatFooter),
		timestamps: g.chatFlag(ctx, platform, chatID, chatTimestamps),
		humanDelay: g.chatFlag(ctx, platform, chatID, chatHumanDelay),
	}
}

func (g *serveGateway) chatFlag(ctx context.Context, platform, chatID, key string) bool {
	raw, found, err := g.store.ChatSetting(ctx, platform, chatID, key)
	if err != nil || !found {
		return false
	}
	on, perr := strconv.ParseBool(strings.TrimSpace(raw))
	if perr != nil {
		sessionsPrintf(g.stderr, "gateway: chat setting %s/%s.%s is not a bool\n", cleanArg(platform), cleanArg(chatID), cleanArg(key))
		return false
	}
	return on
}

func timestampPrefix(now time.Time) string {
	return "[" + now.Format("15:04") + "] "
}

func withTimestamp(text, prefix string) string {
	if prefix == "" || strings.HasPrefix(text, prefix) {
		return text
	}
	return prefix + text
}

func footerText(model string, meta replyMeta, duration time.Duration) string {
	cost := "cost unknown"
	if meta.CostKnown {
		cost = "$" + strconv.FormatFloat(meta.CostUSD, 'f', 6, 64)
	}
	return fmt.Sprintf("\n\n— %s · %d iter · %s · %s", model, meta.Iterations, cost, duration.Round(100*time.Millisecond))
}

func humanDelayAmount(draw func() time.Duration) time.Duration {
	return min(max(humanDelayMin+draw(), humanDelayMin), humanDelayMax)
}

func (g *serveGateway) humanPace(ctx context.Context, enabled bool) error {
	if !enabled {
		return nil
	}
	draw := g.delayRandom
	if draw == nil {
		draw = func() time.Duration { return rand.N(humanDelayMax - humanDelayMin) }
	}
	wait := g.delayWait
	if wait == nil {
		wait = waitReply
	}
	return wait(ctx, humanDelayAmount(draw))
}
