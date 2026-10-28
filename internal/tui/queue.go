package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// enqueue stores a prompt that arrived while a turn was running; nothing is
// dropped. FIFO order is the submission order.
func (m *Model) enqueue(text string) {
	m.queue = append(m.queue, text)
	m.appendNotice(fmt.Sprintf("queued (%d)", len(m.queue)))
}

func (m *Model) queueView() string {
	if len(m.queue) == 0 {
		return "queue: empty"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "queue (%d)", len(m.queue))
	for i, q := range m.queue {
		fmt.Fprintf(&b, "\n%d. %s", i+1, q)
	}
	return b.String()
}

func (m *Model) cmdQueue(args []string) tea.Cmd {
	if len(args) == 0 || args[0] == "list" {
		m.appendNotice(m.queueView())
		return nil
	}
	switch args[0] {
	case "clear":
		m.queue = nil
		m.appendNotice("queue cleared")
	case "add":
		m.queueAdd(args)
	case "rm":
		m.queueRemove(args)
	case "move":
		m.queueMove(args)
	case "edit":
		m.queueEdit(args)
	default:
		text := strings.Join(args, " ")
		if m.busy {
			m.queue = append(m.queue, text)
			m.appendNotice(fmt.Sprintf("queued (%d)", len(m.queue)))
			return nil
		}
		return m.startTurn(text)
	}
	return nil
}

func (m *Model) queueAdd(args []string) {
	if len(args) < 2 {
		m.appendNotice("usage: /queue add <prompt>")
		return
	}
	m.queue = append(m.queue, strings.Join(args[1:], " "))
	m.appendNotice(m.queueView())
}

func (m *Model) queueRemove(args []string) {
	if len(args) != 2 {
		m.appendNotice("usage: /queue rm <N>")
		return
	}
	i, ok := queueIndex(args[1], len(m.queue))
	if !ok {
		return
	}
	m.queue = append(m.queue[:i], m.queue[i+1:]...)
	m.appendNotice(m.queueView())
}

func (m *Model) queueMove(args []string) {
	if len(args) != 3 {
		m.appendNotice("usage: /queue move <A> <B>")
		return
	}
	a, ok := queueIndex(args[1], len(m.queue))
	if !ok {
		return
	}
	b, ok := queueIndex(args[2], len(m.queue))
	if !ok {
		return
	}
	q := m.queue[a]
	m.queue = append(m.queue[:a], m.queue[a+1:]...)
	m.queue = append(m.queue, "")
	copy(m.queue[b+1:], m.queue[b:])
	m.queue[b] = q
	m.appendNotice(m.queueView())
}

func (m *Model) queueEdit(args []string) {
	if len(args) < 3 {
		m.appendNotice("usage: /queue edit <N> <prompt>")
		return
	}
	i, ok := queueIndex(args[1], len(m.queue))
	if !ok {
		return
	}
	m.queue[i] = strings.Join(args[2:], " ")
	m.appendNotice(m.queueView())
}

func queueIndex(raw string, n int) (int, bool) {
	i, err := strconv.Atoi(raw)
	if err != nil || i < 1 || i > n {
		return 0, false
	}
	return i - 1, true
}

func (m *Model) cmdSteer(args []string) tea.Cmd {
	if len(args) == 0 {
		m.appendNotice("usage: /steer <prompt>")
		return nil
	}
	m.appendNotice("steer refused: the loop has no mid-turn injection seam; the running turn is not interrupted")
	return nil
}

func (m *Model) cmdBusy(args []string) tea.Cmd {
	mode := m.busyPolicy
	if mode == "" {
		mode = "queue"
	}
	switch {
	case len(args) == 0 || args[0] == "status":
		m.appendNotice(fmt.Sprintf("busy mode %s; queue %d", mode, len(m.queue)))
	case args[0] == "queue":
		m.busyPolicy = "queue"
		m.appendNotice("busy mode queue (already active)")
	case args[0] == "steer":
		m.appendNotice("busy steer refused: the loop has no mid-turn injection seam")
	case args[0] == "interrupt":
		m.appendNotice("busy interrupt refused: use esc to cancel the running turn")
	default:
		m.appendNotice("usage: /busy [status|queue|steer|interrupt]")
	}
	return nil
}
