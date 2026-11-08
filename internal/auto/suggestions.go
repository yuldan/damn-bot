//go:build !minimal

package auto

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Suggestion is one automatable task the model proposes. It is data only;
// accepting a suggestion is a separate user action.
type Suggestion struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
}

const suggestionsPrompt = "Propose at most five useful recurring automations for this setup. " +
	`Answer with a JSON array of objects {"title": "...", "prompt": "..."} and nothing else.`

// Suggest asks the auxiliary model for automation suggestions. The zero
// config makes no call; malformed or empty JSON is an error, never a silent
// empty list.
func Suggest(ctx context.Context, s *Service, input string) ([]Suggestion, error) {
	if !s.Enabled(Suggestions) {
		return nil, fmt.Errorf("%w: %s", ErrDisabled, Suggestions)
	}
	resp, err := s.Call(ctx, Suggestions, suggestionsPrompt+"\n\n"+input)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(resp.Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	var out []Suggestion
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		return nil, fmt.Errorf("auto: %s response: %w", Suggestions, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrEmptyOutput, Suggestions)
	}
	if len(out) > 5 {
		out = out[:5]
	}
	for i := range out {
		out[i].Title = strings.TrimSpace(out[i].Title)
		out[i].Prompt = strings.TrimSpace(out[i].Prompt)
		if out[i].Title == "" || out[i].Prompt == "" {
			return nil, fmt.Errorf("auto: %s entry %d is incomplete", Suggestions, i)
		}
	}
	return out, nil
}
