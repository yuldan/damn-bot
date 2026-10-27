package tui

import (
	"slices"
	"strings"
)

// Transcript virtualisation (SPEC §13.1): the renderer walks backwards from the
// anchor block and stops once the viewport is full, so a 1.000-block transcript
// costs one frame of visible blocks instead of all of them. scroll counts the
// blocks hidden below the window; 0 follows the tail.

// bodyHeight is the number of transcript lines between the header, the status
// bar, the input area, and the footer. 0 means the terminal size is not known
// yet: the whole transcript is rendered so the first frame and pure-model tests
// never clip silently.
func (m *Model) bodyHeight() int {
	reserved := 4 + len(m.draft)
	if m.height <= reserved {
		return 0
	}
	return m.height - reserved
}

// transcript renders the visible window newest-first while walking back, then
// joins the collected blocks in transcript order.
func (m *Model) transcript() string {
	n := len(m.blocks)
	end := max(0, min(n, n-m.scroll))
	avail := m.bodyHeight()
	visible := make([]string, 0, end)
	used := 0
	for i := end - 1; i >= 0; i-- {
		s := m.renderBlock(&m.blocks[i])
		h := lineCount(s)
		if avail > 0 {
			if len(visible) > 0 && used+h > avail {
				break
			}
			if len(visible) == 0 && h > avail {
				// A single block taller than the window: keep its tail, the
				// part a tail-following reader needs.
				s, h = tailLines(s, avail), avail
			}
		}
		visible = append(visible, s)
		used += h
		if avail > 0 && used >= avail {
			break
		}
	}
	var b strings.Builder
	for _, s := range slices.Backward(visible) {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
	}
	return b.String()
}

// scrollBy pages the transcript up (older) or down (newer). The page is the
// number of blocks currently visible, with a one-block floor; the offset is
// clamped so the first block can always be reached.
func (m *Model) scrollBy(up bool) {
	n := len(m.blocks)
	if n == 0 {
		m.scroll = 0
		return
	}
	page := max(m.visibleBlockCount(), 1)
	if up {
		m.scroll += page
		if m.scroll > n-1 {
			m.scroll = n - 1
		}
		return
	}
	m.scroll -= page
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// visibleBlockCount is the page size of one scroll step at the current
// position. It measures by rendering, exactly like transcript(), and never
// exceeds the blocks that fit.
func (m *Model) visibleBlockCount() int {
	n := len(m.blocks)
	end := max(0, min(n, n-m.scroll))
	avail := m.bodyHeight()
	count, used := 0, 0
	for i := end - 1; i >= 0; i-- {
		h := lineCount(m.renderBlock(&m.blocks[i]))
		if avail > 0 {
			if count > 0 && used+h > avail {
				break
			}
			if count == 0 && h > avail {
				h = avail
			}
		}
		count++
		used += h
		if avail > 0 && used >= avail {
			break
		}
	}
	return count
}

// lineCount is the rendered height of one block in terminal lines.
func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

// tailLines keeps the last n lines of s without splitting the whole block.
func tailLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	end := len(s)
	for range n {
		j := strings.LastIndexByte(s[:end], '\n')
		if j < 0 {
			return s
		}
		end = j
	}
	return s[end+1:]
}
