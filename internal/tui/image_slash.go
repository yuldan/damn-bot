//go:build !minimal

package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// imageSlash attaches one image to the next prompt. The clipboard and file are
// read through host seams, so the TUI performs no I/O; binary bytes never
// reach a notice or a block (UI-008).
func (m *Model) imageSlash(cmd slashCommand) tea.Cmd {
	//exhaustive:ignore
	switch cmd.kind {
	case slashPaste:
		if !m.needArgs(cmd, 0, "usage: /paste") {
			return nil
		}
		clip, res := m.host.Clipboard, m.host.Resources
		return func() tea.Msg {
			img, err := clip.ReadImage()
			if err != nil {
				return attachmentMsg{err: err}
			}
			att, err := res.AttachImage(context.Background(), "clipboard-image", img.Data)
			return attachmentMsg{att: att, err: err}
		}
	case slashImage:
		if !m.needArgs(cmd, 1, "usage: /image <path>") {
			return nil
		}
		path, res := cmd.args[0], m.host.Resources
		return func() tea.Msg {
			att, err := res.AttachFile(context.Background(), path)
			return attachmentMsg{att: att, err: err}
		}
	}
	return nil
}
