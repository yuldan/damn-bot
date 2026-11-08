//go:build !minimal

package auto

import (
	"fmt"
	"slices"
	"strings"
)

// Blueprint is one built-in automation template. Slots are the only parts a
// caller may fill; unknown names stay closed.
type Blueprint struct {
	Name        string
	Description string
	Slots       []string
	Steps       []string
}

func blueprintCatalog() []Blueprint {
	return []Blueprint{
		{
			Name:        "daily-review",
			Description: "Review the day's sessions and propose durable memories",
			Slots:       []string{"time"},
			Steps:       []string{"collect sessions since 00:00", "run review", "quarantine proposals"},
		},
		{
			Name:        "pr-watch",
			Description: "Watch a repository and summarise new pull requests",
			Slots:       []string{"repo", "interval"},
			Steps:       []string{"poll new pull requests", "summarise", "notify the owner"},
		},
	}
}

// BlueprintCatalog returns a copy of the built-in catalog.
func BlueprintCatalog() []Blueprint { return slices.Clone(blueprintCatalog()) }

// Instantiate validates the template name and its slots for the blueprints
// feature. A disabled feature returns [ErrDisabled] before any lookup.
func Instantiate(s *Service, name string, slots map[string]string) (Blueprint, error) {
	if !s.Enabled(Blueprints) {
		return Blueprint{}, fmt.Errorf("%w: %s", ErrDisabled, Blueprints)
	}
	for _, bp := range blueprintCatalog() {
		if bp.Name != name {
			continue
		}
		if len(slots) != len(bp.Slots) {
			return Blueprint{}, fmt.Errorf("%w: %s expects slots %v", ErrConfig, name, bp.Slots)
		}
		for _, slot := range bp.Slots {
			if strings.TrimSpace(slots[slot]) == "" {
				return Blueprint{}, fmt.Errorf("%w: %s slot %q is empty", ErrConfig, name, slot)
			}
		}
		return bp, nil
	}
	return Blueprint{}, fmt.Errorf("%w: blueprint %q", ErrUnknownFeature, name)
}
