//go:build !minimal

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
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
//
//nolint:dupl // runner load/store identik dengan runProject; boilerplate sengaja eksplisit per surface.
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
	say(w, "  list  install <name> --from FILE|--url MANIFEST [--description TEXT]  select <name>  show <name>\n")
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
	url := fs.String("url", "", "opt-in petdex manifest URL")
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
		return petsRemoteInstall(pos[0], *url, st, stdout)
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
	return petsStore(st, &pet, stdout), nil
}

// petManifest is the opt-in petdex index: one entry per installable pet.
type petManifest struct {
	Pets []petManifestEntry `json:"pets"`
}

type petManifestEntry struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Style       string `json:"style"`
	Asset       string `json:"asset"`
	SHA256      string `json:"sha256"`
}

// petsRemoteInstall fetches the manifest and the asset over the petdex egress
// lane, then records metadata and the digest only: the runtime never loads an
// external asset (CLI-050, UI-010).
func petsRemoteInstall(name, manifestURL string, st *f8PetsState, stdout io.Writer) (bool, error) {
	if err := petsRemoteURL(manifestURL); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := netx.New(netx.Config{NoBackground: true}).Client("petdex", netx.Tool)
	entry, err := petsManifestFetch(ctx, client, manifestURL, name)
	if err != nil {
		return false, err
	}
	data, err := petsAssetFetch(ctx, client, manifestURL, entry)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if entry.SHA256 != "" && !strings.EqualFold(entry.SHA256, digest) {
		return false, f8Unavail("petdex", "asset digest does not match the manifest")
	}
	if p := petsFind(st, name); p != nil && p.SHA256 == digest {
		say(stdout, "already installed: %s\n", name)
		return false, nil
	}
	pet := f8Pet{
		Name: name, Style: petStyle(entry), Description: petDescription(entry, name), Scale: "medium",
		SHA256: digest, Bytes: int64(len(data)), InstalledAt: time.Now().Unix(),
	}
	return petsStore(st, &pet, stdout), nil
}

func petsManifestFetch(ctx context.Context, client *netx.Client, manifestURL, name string) (*petManifestEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return nil, f8BadArg("petdex manifest URL is invalid")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, f8Unavail("petdex", "manifest fetch failed")
	}
	defer proxyCloseBody(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, f8Unavail("petdex", fmt.Sprintf("manifest returned HTTP %d", resp.StatusCode))
	}
	var m petManifest
	if derr := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&m); derr != nil {
		return nil, f8Unavail("petdex", "manifest is not valid JSON")
	}
	entry := petsManifestFind(&m, name)
	if entry == nil {
		return nil, f8BadArg("pet %q is not in the petdex manifest", cleanArg(name))
	}
	return entry, nil
}

func petsAssetFetch(ctx context.Context, client *netx.Client, manifestURL string, entry *petManifestEntry) ([]byte, error) {
	assetURL, err := petdexResolve(manifestURL, entry.Asset)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, f8BadArg("petdex asset URL is invalid")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, f8Unavail("petdex", "asset fetch failed")
	}
	defer proxyCloseBody(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, f8Unavail("petdex", fmt.Sprintf("asset returned HTTP %d", resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f8PetAssetMax+1))
	if err != nil {
		return nil, f8Unavail("petdex", "asset read failed")
	}
	if len(data) > f8PetAssetMax {
		return nil, f8BadArg("pet asset is larger than %d bytes", f8PetAssetMax)
	}
	return data, nil
}

func petStyle(entry *petManifestEntry) string {
	if entry.Style == "" {
		return "terminal"
	}
	return entry.Style
}

func petDescription(entry *petManifestEntry, name string) string {
	if entry.Description != "" {
		return entry.Description
	}
	if entry.Name != "" {
		return entry.Name
	}
	return name
}

// petsStore replaces or appends one pet and keeps the scale of a reinstall.
func petsStore(st *f8PetsState, pet *f8Pet, stdout io.Writer) bool {
	if p := petsFind(st, pet.Name); p != nil {
		pet.Scale = p.Scale
		*p = *pet
	} else {
		st.Items = append(st.Items, *pet)
	}
	slices.SortFunc(st.Items, func(a, b f8Pet) int { return strings.Compare(a.Name, b.Name) })
	say(stdout, "installed %s sha256=%s bytes=%d style=%s\n", pet.Name, pet.SHA256, pet.Bytes, pet.Style)
	return true
}

func petsManifestFind(m *petManifest, name string) *petManifestEntry {
	for i := range m.Pets {
		if m.Pets[i].Slug == name || m.Pets[i].Name == name {
			return &m.Pets[i]
		}
	}
	return nil
}

func petdexResolve(base, ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", f8BadArg("petdex asset reference is invalid")
	}
	if !u.IsAbs() {
		b, berr := url.Parse(base)
		if berr != nil {
			return "", f8BadArg("petdex manifest URL is invalid")
		}
		u = b.ResolveReference(u)
	}
	if err := petsRemoteURL(u.String()); err != nil {
		return "", err
	}
	return u.String(), nil
}

// petsRemoteURL accepts https, or loopback http for the fake test server.
// Any other scheme or host is refused before a dial.
func petsRemoteURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return f8BadArg("petdex URL is invalid")
	}
	if u.Scheme == "https" || (u.Scheme == "http" && proxyLoopback(u.Hostname())) {
		return nil
	}
	return f8BadArg("petdex URL must be https or a loopback http URL")
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
