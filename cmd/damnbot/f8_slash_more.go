//go:build !minimal

package main

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// journeySlash implements /journey, /learning, /memory-graph (SLASH-027).
func (a *f8SlashAdapter) journeySlash(dir string, owner bool, args []string) string {
	st := f8JourneyState{Notes: map[string]string{}}
	if err := f8Load(f8StorePath(dir, "journey.json"), &st); err != nil {
		return "journey: " + cleanLine(err.Error())
	}
	if st.Notes == nil {
		st.Notes = map[string]string{}
	}
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	var b strings.Builder
	switch sub {
	case "list":
		entries, err := journeyEntries(dir, &st)
		if err != nil {
			return "journey: " + cleanLine(err.Error())
		}
		start := max(0, len(entries)-10)
		for _, e := range entries[start:] {
			fmt.Fprintf(&b, "%s %s %s\n", time.Unix(e.At, 0).UTC().Format("2006-01-02"), e.Action, e.Name)
		}
		if len(entries) == 0 {
			return "journey: no entries"
		}
	case "delete":
		if msg := f8OwnerOnly(owner, "journey"); msg != "" {
			return msg
		}
		changed, err := journeyDelete(args[1:], &st, &b, io.Discard)
		return a.finishJourney(dir, &st, changed, err, &b)
	case "edit":
		if msg := f8OwnerOnly(owner, "journey"); msg != "" {
			return msg
		}
		changed, err := journeyEdit(args[1:], dir, &st, &b, io.Discard)
		return a.finishJourney(dir, &st, changed, err, &b)
	default:
		return "usage: /journey [list|delete <key>|edit <key> <note>]"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (a *f8SlashAdapter) finishJourney(dir string, st *f8JourneyState, changed bool, err error, b *strings.Builder) string {
	if err != nil {
		return "journey: " + cleanLine(err.Error())
	}
	if changed {
		if serr := f8Save(f8StorePath(dir, "journey.json"), st); serr != nil {
			return "journey: " + cleanLine(serr.Error())
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// petSlash implements /pet toggle|list|scale|off|show (SLASH-072).
func (a *f8SlashAdapter) petSlash(dir string, owner bool, args []string) string {
	st := f8PetsState{}
	if err := f8Load(f8StorePath(dir, "pets.json"), &st); err != nil {
		return "pet: " + cleanLine(err.Error())
	}
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	var b strings.Builder
	switch sub {
	case "list", "show":
		err := petsListOrShow(sub, args[1:], &st, &b)
		return a.finishPets(dir, &st, false, err, &b)
	case "off":
		changed, err := petsOff(args[1:], &st, &b, io.Discard)
		return a.finishPets(dir, &st, changed, err, &b)
	case "scale":
		changed, err := petsScale(args[1:], &st, &b, io.Discard)
		return a.finishPets(dir, &st, changed, err, &b)
	case "toggle", "select":
		return a.petToggle(dir, owner, sub, args[1:], &st)
	default:
		return "usage: /pet [list|toggle <name>|select <name>|show <name>|scale <name> small|medium|large|off]"
	}
}

func petsListOrShow(sub string, args []string, st *f8PetsState, out io.Writer) error {
	if sub == "show" {
		return petsShow(args, st, out, io.Discard)
	}
	return petsList(args, st, out, io.Discard)
}

func (a *f8SlashAdapter) petToggle(dir string, owner bool, sub string, args []string, st *f8PetsState) string {
	if msg := f8OwnerOnly(owner, "pet"); msg != "" {
		return msg
	}
	if len(args) != 1 {
		return "usage: /pet " + sub + " <name>"
	}
	if sub == "toggle" && st.Active == args[0] {
		changed, err := petsOff(nil, st, io.Discard, io.Discard)
		return a.finishPets(dir, st, changed, err, nil)
	}
	changed, err := petsSelect(args, st, io.Discard, io.Discard)
	return a.finishPets(dir, st, changed, err, nil)
}

func (a *f8SlashAdapter) finishPets(dir string, st *f8PetsState, changed bool, err error, b *strings.Builder) string {
	if err != nil {
		return "pet: " + cleanLine(err.Error())
	}
	if changed {
		if serr := f8Save(f8StorePath(dir, "pets.json"), st); serr != nil {
			return "pet: " + cleanLine(serr.Error())
		}
	}
	if b == nil {
		return "pet " + st.Active
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// hatchSlash implements /hatch, /generate-pet (SLASH-073) with a local,
// deterministic pet. Network generation is opt-in and unavailable here.
