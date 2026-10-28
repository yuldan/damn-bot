package tui

import (
	"fmt"
	"strings"
)

// statusBar renders the minimal SPEC §13.1 status line: model, iteration,
// turn/session cost, and the visible-fallback badge (P7). It reports only
// values the host has actually delivered; a missing provider cost stays "—".
func (m *Model) statusBar() string {
	parts := []string{
		m.host.Config.Snapshot().Model,
		fmt.Sprintf("iter %d", m.usage.Iterations),
		m.costSummary(),
	}
	if m.fallback {
		parts = append(parts, "FALLBACK")
	}
	if m.plan {
		parts = append(parts, "PLAN")
	}
	line := m.styles.status.Render(strings.Join(parts, " · "))
	return m.limitWidth(line)
}

// costSummary renders the current turn and the accumulated session cost.
func (m *Model) costSummary() string {
	turn, turnOK := m.turnCost()
	session, sessionOK := m.sessionCost()
	return fmt.Sprintf("turn %s / session %s", usd(turn, turnOK), usd(session, sessionOK))
}

// turnCost returns the loop's turn total, if the provider reported one.
func (m *Model) turnCost() (float64, bool) {
	if m.usage.CostUSD == nil {
		return 0, false
	}
	return *m.usage.CostUSD, true
}

// sessionCost adds every settled turn to the live turn total.
func (m *Model) sessionCost() (float64, bool) {
	total, ok := m.settledCost, m.settledKnown
	if !m.turnSettled {
		if c := m.usage.CostUSD; c != nil {
			total += *c
			ok = true
		}
	}
	return total, ok
}

// usd formats a cost; an unknown cost is an explicit dash, never $0.00.
func usd(v float64, ok bool) string {
	if !ok {
		return "—"
	}
	return fmt.Sprintf("$%.2f", v)
}
