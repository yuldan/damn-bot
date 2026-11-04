//go:build minimal

package tui

func (m *Model) handlePaste(string) {}

func (m *Model) applyAttachment(attachmentMsg) {}

func attachmentPreview(Attachment) string { return "" }
