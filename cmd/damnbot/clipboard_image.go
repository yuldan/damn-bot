//go:build !minimal

// Clipboard image read and attachment seams (SLASH-098/SLASH-099, UI-008).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"

	"github.com/damnbot/damnbot/internal/tui"
)

var (
	// errNoClipboardImage means every external clipboard reader ran but none
	// held an image; it is a notice, not a crash.
	errNoClipboardImage = errors.New("no image in the clipboard")
	// errClipboardTooLarge caps one clipboard read before it is allocated.
	errClipboardTooLarge = errors.New("clipboard image exceeds 32 MiB")
)

// maxClipboardImage matches the preflight input cap.
const maxClipboardImage = 32 << 20

type clipboardCmd struct {
	name string
	args []string
}

// clipboardImageCommands lists external readers in preference order. The
// binaries are optional; a missing executable simply moves to the next one.
func clipboardImageCommands(goos string) []clipboardCmd {
	switch goos {
	case "linux":
		return []clipboardCmd{
			{"wl-paste", []string{"--no-newline", "--type", "image/png"}},
			{"xclip", []string{"-selection", "clipboard", "-t", "image/png", "-o"}},
		}
	case "darwin":
		return []clipboardCmd{{"pngpaste", []string{"-"}}}
	default:
		return nil
	}
}

// readClipboardImage runs one external reader and never prints clipboard bytes
// in an error. The output is capped at maxClipboardImage.
func readClipboardImage(ctx context.Context, goos string, look func(string) (string, error), run func(context.Context, string, ...string) ([]byte, error)) ([]byte, error) {
	for _, c := range clipboardImageCommands(goos) {
		if _, err := look(c.name); err != nil {
			continue
		}
		data, err := run(ctx, c.name, c.args...)
		if err == nil && len(data) > 0 {
			return data, nil
		}
	}
	return nil, errNoClipboardImage
}

// runClipboardCmd reads at most maxClipboardImage+1 bytes so an oversized
// image is rejected before it can grow the heap.
func runClipboardCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- fixed clipboard utility args
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("clipboard reader %s pipe: %w", name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("clipboard reader %s start: %w", name, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(out, maxClipboardImage+1))
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil {
		return nil, fmt.Errorf("clipboard reader %s failed", name)
	}
	if len(data) > maxClipboardImage {
		return nil, errClipboardTooLarge
	}
	return data, nil
}

// ReadImage implements tui.Clipboard through the external utilities.
func (clipboardAdapter) ReadImage() (tui.ClipboardImage, error) {
	data, err := readClipboardImage(context.Background(), runtime.GOOS, exec.LookPath, runClipboardCmd)
	if err != nil {
		return tui.ClipboardImage{}, err
	}
	return tui.ClipboardImage{Data: data}, nil
}
