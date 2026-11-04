//go:build minimal

package main

import (
	"context"
	"errors"

	"github.com/damnbot/damnbot/internal/tui"
)

var errNoClipboardImage = errors.New("no image in the clipboard")

func (clipboardAdapter) ReadImage() (tui.ClipboardImage, error) {
	return tui.ClipboardImage{}, errNoClipboardImage
}

func (r *tuiRunner) AttachImage(context.Context, string, []byte) (tui.Attachment, error) {
	return tui.Attachment{}, errors.New("image attachments are unavailable in the minimal build")
}

func (r *tuiRunner) AttachFile(context.Context, string) (tui.Attachment, error) {
	return tui.Attachment{}, errors.New("image attachments are unavailable in the minimal build")
}
