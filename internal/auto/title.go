//go:build !minimal

package auto

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrEmptyOutput rejects a provider answer that carries no usable text; the
// feature never writes an empty label or suggestion.
var ErrEmptyOutput = errors.New("auto: empty output")

// ErrNoStore means a feature that persists a result has no store wired.
var ErrNoStore = errors.New("auto: no store")

// TitleStore persists the generated session title. The caller owns storage;
// this package never writes the database directly.
type TitleStore interface {
	SetTitle(ctx context.Context, sessionID, title string) error
}

const titlePrompt = "Give this conversation a short title of at most six words. " +
	"Answer with the title only, no quotes, no punctuation at the end."

// GenerateTitle asks the auxiliary model for a session title and stores it.
// A disabled feature returns [ErrDisabled] before the provider or store is
// touched; empty output never reaches the store.
func GenerateTitle(ctx context.Context, s *Service, store TitleStore, sessionID, firstMessage string) (string, error) {
	if !s.Enabled(Title) {
		return "", fmt.Errorf("%w: %s", ErrDisabled, Title)
	}
	if store == nil {
		return "", fmt.Errorf("%w: %s", ErrNoStore, Title)
	}
	resp, err := s.Call(ctx, Title, titlePrompt+"\n\n"+firstMessage)
	if err != nil {
		return "", err
	}
	title := strings.Join(strings.Fields(resp.Content), " ")
	if title == "" {
		return "", fmt.Errorf("%w: %s", ErrEmptyOutput, Title)
	}
	title = trimTitle(title, 80)
	if err := store.SetTitle(ctx, sessionID, title); err != nil {
		return "", fmt.Errorf("auto: %s store: %w", Title, err)
	}
	return title, nil
}

func trimTitle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(slices.Clone(r[:max])))
}
