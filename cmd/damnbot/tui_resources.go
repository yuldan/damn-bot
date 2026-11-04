//go:build !minimal

// TUI image attachment seam (SLASH-098/099, UI-008): preflight and workspace
// path gate live here so internal/tui stays I/O free (E-09).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/media/preflight"
	"github.com/damnbot/damnbot/internal/tui"
)

// AttachImage preflights one clipboard or tool image and publishes it.
func (r *tuiRunner) AttachImage(ctx context.Context, name string, data []byte) (tui.Attachment, error) {
	return r.attachImage(ctx, name, data)
}

// AttachFile reads one local image under the workspace root and attaches it.
func (r *tuiRunner) AttachFile(ctx context.Context, path string) (tui.Attachment, error) {
	root, err := r.workspaceRoot()
	if err != nil {
		return tui.Attachment{}, err
	}
	data, err := readWorkspaceImage(root, path)
	if err != nil {
		return tui.Attachment{}, err
	}
	return r.attachImage(ctx, path, data)
}

// workspaceRoot is the process working directory unless a test injects one.
func (r *tuiRunner) workspaceRoot() (string, error) {
	if r.deps.workspace != "" {
		return r.deps.workspace, nil
	}
	root, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	return root, nil
}

// readWorkspaceImage applies the workspace gate before reading, then caps the
// file at the preflight input limit.
func readWorkspaceImage(root, path string) (data []byte, err error) {
	rel, err := workspaceRelative(root, path)
	if err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	st, err := dir.Stat(rel)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", path, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("image %s is a directory", path)
	}
	if st.Size() > preflight.MaxInputBytes {
		return nil, fmt.Errorf("image %s exceeds 32 MiB", path)
	}
	data, err = dir.ReadFile(rel)
	if err != nil {
		return nil, fmt.Errorf("image %s: %w", path, err)
	}
	return data, nil
}

// workspaceRelative rejects every path that resolves outside root. os.OpenRoot
// then prevents a symlink inside the workspace from escaping it.
func workspaceRelative(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("image path is empty")
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, path)
	}
	rel, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("image %s is outside the workspace", path)
	}
	return rel, nil
}

// attachImage preflights an encoded image and returns its blob reference.
func (r *tuiRunner) attachImage(ctx context.Context, name string, data []byte) (tui.Attachment, error) {
	if len(data) > preflight.MaxInputBytes {
		return tui.Attachment{}, fmt.Errorf("image %s exceeds 32 MiB", name)
	}
	res, err := preflight.Preflight(data, preflight.Default())
	if err != nil {
		return tui.Attachment{}, fmt.Errorf("image %s: %w", name, err)
	}
	if name == "" {
		name = "image"
	}
	return r.SpillResource(ctx, name, res.MIME, res.Data)
}
