//go:build !minimal

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

const f8PetAssetMax = 1 << 20

// f8Pet is one installed mascot. The asset itself is never copied: install
// records its digest and metadata only, so the runtime loads no external
// asset (UI-010, CLI-050).
type f8Pet struct {
	Name        string `json:"name"`
	Style       string `json:"style"`
	Description string `json:"description"`
	Scale       string `json:"scale"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	InstalledAt int64  `json:"installed_at"`
}

// f8PetsState is the <data_dir>/pets.json store.
type f8PetsState struct {
	Active string  `json:"active,omitempty"`
	Items  []f8Pet `json:"items"`
}

// runPets implements `damnbot pets` (CLI-050).
func runPets(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pets", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory (default: profile data dir)")
	if err := fs.Parse(args); err != nil {
		return f8ExitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		petsUsage(stderr)
		return f8ExitUsage
	}
	dir, err := f8Dir(*dataDir)
	if err != nil {
		return f8CmdFail(stderr, "pets", err)
	}
	st := f8PetsState{}
	if lerr := f8Load(f8StorePath(dir, "pets.json"), &st); lerr != nil {
		return f8CmdFail(stderr, "pets", lerr)
	}
	changed, err := petsCommand(rest, &st, stdout, stderr)
	if err != nil {
		if errors.Is(err, errF8Usage) {
			return f8ExitUsage
		}
		return f8CmdFail(stderr, "pets", err)
	}
	if changed {
		if err := f8Save(f8StorePath(dir, "pets.json"), &st); err != nil {
			return f8CmdFail(stderr, "pets", err)
		}
	}
	return f8ExitOK
}

func petsUsage(w io.Writer) {
	say(w, "usage: damnbot pets [--data-dir DIR] <sub>\n")
	say(w, "  list  install <name> --from FILE [--description TEXT]  select <name>  show <name>\n")
	say(w, "  off  scale <name> small|medium|large  remove <name>  doctor\n")
}

func petsCommand(rest []string, st *f8PetsState, stdout, stderr io.Writer) (bool, error) {
	switch rest[0] {
	case "list":
		return false, petsList(rest[1:], st, stdout, stderr)
	case "install":
		return petsInstall(rest[1:], st, stdout, stderr)
	case "select":
		return petsSelect(rest[1:], st, stdout, stderr)
	case "show":
		return false, petsShow(rest[1:], st, stdout, stderr)
	case "off":
		return petsOff(rest[1:], st, stdout, stderr)
	case "scale":
		return petsScale(rest[1:], st, stdout, stderr)
	case "remove":
		return petsRemove(rest[1:], st, stdout, stderr)
	case "doctor":
		return false, petsDoctor(rest[1:], st, stdout, stderr)
	default:
		say(stderr, "pets: unknown subcommand %q\n", cleanArg(rest[0]))
		petsUsage(stderr)
		return false, errF8Usage
	}
}

func petsList(args []string, st *f8PetsState, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		petsUsage(stderr)
		return errF8Usage
	}
	items := slices.Clone(st.Items)
	slices.SortFunc(items, func(a, b f8Pet) int { return strings.Compare(a.Name, b.Name) })
	for _, p := range items {
		mark := " "
		if st.Active == p.Name {
			mark = "*"
		}
		say(stdout, "%s%s scale=%s style=%s\n", mark, p.Name, p.Scale, p.Style)
	}
	return nil
}

func petsInstall(args []string, st *f8PetsState, stdout, stderr io.Writer) (bool, error) {
	fs := flag.NewFlagSet("pets install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "local pet asset (<= 1 MiB)")
	url := fs.String("url", "", "opt-in petdex URL (unavailable in this build)")
	desc := fs.String("description", "", "one-line description")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return false, errF8Usage
	}
	if len(pos) != 1 || (*from == "") == (*url == "") {
		petsUsage(stderr)
		return false, errF8Usage
	}
	if *url != "" {
		return false, f8Unavail("petdex", "remote install is not available; use --from FILE")
	}
	if err := petsNameOK(pos[0]); err != nil {
		return false, err
	}
	data, err := os.ReadFile(*from) // #nosec G304 -- operator-supplied pet asset
	if err != nil {
		return false, fmt.Errorf("pet asset: %w", err)
	}
	if len(data) > f8PetAssetMax {
		return false, f8BadArg("pet asset is larger than %d bytes", f8PetAssetMax)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if p := petsFind(st, pos[0]); p != nil && p.SHA256 == digest {
		say(stdout, "already installed: %s\n", pos[0])
		return false, nil
	}
	pet := f8Pet{Name: pos[0], Style: "terminal", Description: *desc, Scale: "medium", SHA256: digest, Bytes: int64(len(data)), InstalledAt: time.Now().Unix()}
	if p := petsFind(st, pos[0]); p != nil {
		pet.Scale = p.Scale
		*p = pet
	} else {
		st.Items = append(st.Items, pet)
	}
	slices.SortFunc(st.Items, func(a, b f8Pet) int { return strings.Compare(a.Name, b.Name) })
	say(stdout, "installed %s sha256=%s bytes=%d\n", pet.Name, pet.SHA256, pet.Bytes)
	return true, nil
}

func petsSelect(args []string, st *f8PetsState, stdout, stderr io.Writer) (bool, error) {
	if len(args) != 1 {
		petsUsage(stderr)
		return false, errF8Usage
	}
	if petsFind(st, args[0]) == nil {
		return false, fmt.Errorf("pet %q is not installed", args[0])
	}
	if st.Active == args[0] {
		return false, nil
	}
	st.Active = args[0]
	say(stdout, "selected %s\n", args[0])
	return true, nil
}

func petsShow(args []string, st *f8PetsState, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		petsUsage(stderr)
		return errF8Usage
	}
	p := petsFind(st, args[0])
	if p == nil {
		return fmt.Errorf("pet %q is not installed", args[0])
	}
	say(stdout, "name: %s\nstyle: %s\nscale: %s\ndescription: %s\nsha256: %s\nbytes: %d\n", p.Name, p.Style, p.Scale, p.Description, p.SHA256, p.Bytes)
	return nil
}

func petsOff(args []string, st *f8PetsState, stdout, stderr io.Writer) (bool, error) {
	if len(args) != 0 {
		petsUsage(stderr)
		return false, errF8Usage
	}
	if st.Active == "" {
		return false, nil
	}
	st.Active = ""
	say(stdout, "pet off\n")
	return true, nil
}

func petsScale(args []string, st *f8PetsState, stdout, stderr io.Writer) (bool, error) {
	if len(args) != 2 || !slices.Contains([]string{"small", "medium", "large"}, args[1]) {
		petsUsage(stderr)
		return false, errF8Usage
	}
	p := petsFind(st, args[0])
	if p == nil {
		return false, fmt.Errorf("pet %q is not installed", args[0])
	}
	if p.Scale == args[1] {
		return false, nil
	}
	p.Scale = args[1]
	say(stdout, "scale %s = %s\n", p.Name, p.Scale)
	return true, nil
}

func petsRemove(args []string, st *f8PetsState, stdout, stderr io.Writer) (bool, error) {
	if len(args) != 1 {
		petsUsage(stderr)
		return false, errF8Usage
	}
	i := slices.IndexFunc(st.Items, func(p f8Pet) bool { return p.Name == args[0] })
	if i < 0 {
		return false, nil
	}
	st.Items = slices.Delete(st.Items, i, i+1)
	if st.Active == args[0] {
		st.Active = ""
	}
	say(stdout, "removed %s\n", args[0])
	return true, nil
}

func petsDoctor(args []string, st *f8PetsState, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		petsUsage(stderr)
		return errF8Usage
	}
	seen := map[string]bool{}
	for _, p := range st.Items {
		if seen[p.Name] {
			return fmt.Errorf("duplicate pet %q", p.Name)
		}
		seen[p.Name] = true
		if len(p.SHA256) != 64 || p.Bytes < 0 || !slices.Contains([]string{"small", "medium", "large"}, p.Scale) {
			return fmt.Errorf("pet %q has invalid metadata", p.Name)
		}
	}
	say(stdout, "check store ok pets=%d\n", len(st.Items))
	if st.Active == "" {
		say(stdout, "check active none\n")
		return nil
	}
	if !seen[st.Active] {
		return fmt.Errorf("active pet %q is missing", st.Active)
	}
	say(stdout, "check active ok pet=%s\n", st.Active)
	return nil
}

func petsFind(st *f8PetsState, name string) *f8Pet {
	for i := range st.Items {
		if st.Items[i].Name == name {
			return &st.Items[i]
		}
	}
	return nil
}

func petsNameOK(name string) error {
	if name == "" || len(name) > 64 {
		return f8BadArg("pet name must be 1..64 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == '/' || r == '\\' {
			return f8BadArg("pet name %q contains a path separator or control rune", name)
		}
	}
	return nil
}
