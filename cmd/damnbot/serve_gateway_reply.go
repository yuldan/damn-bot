package main

import (
	"context"
	"fmt"
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
	m.Iterations = max(m.Iterations, u.Iterations)
	if u.CostUSD != nil {
		m.CostUSD, m.CostKnown = *u.CostUSD, true
	}
}

func (g *serveGateway) chatFlag(ctx context.Context, chatID, key string) bool {
	raw, found, err := g.store.ChatSetting(ctx, "telegram", chatID, key)
	on, perr := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && found && perr == nil && on
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
	return g.delayWait(ctx, humanDelayAmount(g.delayRandom))
}
