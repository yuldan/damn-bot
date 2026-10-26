package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/damnbot/damnbot/internal/config"
)

// globalFlags adalah flag global SPEC §10 yang diparse SEBELUM dispatch:
// `--profile <name>` bekerja baik di depan perintah (`damnbot --profile p
// run`) maupun tepat sesudah nama perintah (`damnbot run --profile p`).
// Setelah posisi itu argumen diperlakukan sebagai milik perintah, sehingga
// prompt atau path yang kebetulan berbunyi "--profile" tidak dimakan.
type globalFlags struct {
	profile string
}

// invocation is the process-wide context of one CLI run: which profile the
// global `--profile` selected. Path helpers read it; run() is the only writer,
// so a value can never be observed half-written.
type invocation struct{ profile string }

// activeInvocation is the one piece of process-wide state the root command
// owns (E-10 allowlist): an invocation has exactly one global profile, and
// every command resolves its config/data dir from it.
var activeInvocation atomic.Pointer[invocation] //nolint:gochecknoglobals // process-wide --profile of one CLI invocation

func setActiveProfile(name string) { activeInvocation.Store(&invocation{profile: name}) }

// activeProfile mengembalikan profil yang dipilih `--profile` ("" = default).
func activeProfile() string {
	if inv := activeInvocation.Load(); inv != nil {
		return inv.profile
	}
	return ""
}

// parseGlobalArgs memisahkan flag global dari perintah. Nilai profil
// divalidasi dengan loader config (satu sumber aturan nama profil,
// [a-z0-9][a-z0-9_-]{0,63}).
func parseGlobalArgs(args []string) (globalFlags, []string, error) {
	var g globalFlags
	out := make([]string, 0, len(args))
	i := 0
	for i < len(args) && isProfileFlag(args[i]) {
		if err := g.takeProfile(args, &i); err != nil {
			return globalFlags{}, nil, err
		}
	}
	if i < len(args) && !strings.HasPrefix(args[i], "-") {
		out = append(out, args[i])
		i++
		if i < len(args) && isProfileFlag(args[i]) {
			if err := g.takeProfile(args, &i); err != nil {
				return globalFlags{}, nil, err
			}
		}
	}
	out = append(out, args[i:]...)
	if g.profile != "" {
		if err := validateProfile(g.profile); err != nil {
			return globalFlags{}, nil, err
		}
	}
	return g, out, nil
}

func isProfileFlag(s string) bool {
	return s == "--profile" || strings.HasPrefix(s, "--profile=")
}

// takeProfile consumes the flag at args[*i]; *i advances past flag and value.
func (g *globalFlags) takeProfile(args []string, i *int) error {
	s := args[*i]
	value := strings.TrimPrefix(s, "--profile=")
	if s == "--profile" {
		if *i+1 >= len(args) {
			return errors.New("--profile needs a name")
		}
		value = args[*i+1]
		*i++
	}
	*i++
	if value == "" {
		return errors.New("--profile needs a name")
	}
	if g.profile != "" {
		return errors.New("--profile given twice")
	}
	g.profile = value
	return nil
}

func validateProfile(name string) error {
	loader, err := config.New(config.OS())
	if err != nil {
		return fmt.Errorf("config deps: %w", err)
	}
	if _, perr := loader.Paths(name); perr != nil {
		return fmt.Errorf("--profile: %w", perr)
	}
	return nil
}
