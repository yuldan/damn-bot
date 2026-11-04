//go:build !minimal

package tui

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// pasteCollapseRunes is the longest paste kept inline; longer content is
// collapsed to a placeholder and expanded once on submit (UI-008).
const pasteCollapseRunes = 2000

// handlePaste inserts a paste; a long one is shown collapsed, never dropped.
func (m *Model) handlePaste(content string) {
	if content == "" {
		return
	}
	if utf8.RuneCountInString(content) <= pasteCollapseRunes {
		m.input.SetValue(m.input.Value() + content)
		m.input.CursorEnd()
		return
	}
	m.pasted = content
	m.pastePlaceholder = fmt.Sprintf("[pasted %d chars]", utf8.RuneCountInString(content))
	m.input.SetValue(m.input.Value() + m.pastePlaceholder)
	m.input.CursorEnd()
	m.appendNotice("pasted " + strconv.Itoa(utf8.RuneCountInString(content)) + " chars (collapsed; expanded on send)")
}

// attachmentPreview describes one image without its binary content.
func attachmentPreview(a Attachment) string {
	size := max(a.Size, 0)
	return fmt.Sprintf("image %s (%s, %s)", a.Name, a.MIME, formatSize(int(size)))
}

// applyAttachment records one successful /paste or /image result.
func (m *Model) applyAttachment(msg attachmentMsg) {
	if msg.err != nil {
		m.appendNotice("image attach failed: " + msg.err.Error())
		return
	}
	m.pendingAttachments = append(m.pendingAttachments, msg.att)
	m.appendNotice("attached " + attachmentPreview(msg.att) + "; it will be sent with the next prompt")
}
