//go:build !minimal

package main

import (
	"github.com/damnbot/damnbot/internal/tui"
)

// tuiDecor reads the opt-in mascot (pets.json) and personality preset
// (personality.json) on every frame. Both files default to absent/off, so a
// fresh profile renders no badge and reads no external asset (UI-010/011).
type tuiDecor struct{ dir string }

// newTUIDecor builds the read-only badge source.
func newTUIDecor(dataDir string) tui.Decor { return tuiDecor{dir: dataDir} }

// Pet returns the selected mascot and its scale.
func (d tuiDecor) Pet() (string, string) {
	st := f8PetsState{}
	if err := f8Load(f8StorePath(d.dir, "pets.json"), &st); err != nil {
		return "", ""
	}
	for _, item := range st.Items {
		if item.Name == st.Active {
			return st.Active, item.Scale
		}
	}
	return "", ""
}

// Persona returns the stored preset.
func (d tuiDecor) Persona() string {
	state := struct {
		Current string `json:"current"`
	}{}
	if err := f8Load(f8StorePath(d.dir, "personality.json"), &state); err != nil {
		return ""
	}
	return state.Current
}
