package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/secret"
)

// setupSecrets implements `damnbot setup secrets set|rotate|ls [--refs]`
// (GAP-OPS-04, ADR-223). It never prints a secret value.
func setupSecrets(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, "setup secrets: set|rotate|ls expected\n")
		return 2
	}
	switch args[0] {
	case "ls":
		return secretsList(args[1:], stdout, stderr)
	case "set", "rotate":
		return secretsPut(args[0], args[1:], stdout, stderr)
	default:
		say(stderr, "setup secrets: unknown subcommand %q\n", cleanArg(args[0]))
		return 2
	}
}

func secretsList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("setup secrets ls", flag.ContinueOnError)
	flags.SetOutput(stderr)
	refs := flags.Bool("refs", false, "name x profile matrix, no values")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		say(stderr, "setup secrets ls: unexpected argument\n")
		return 2
	}
	if *refs {
		profiles, err := secretProfileStores()
		if err != nil {
			return secretsFail(stderr, err)
		}
		return secretsMatrix(profiles, stdout, stderr)
	}
	st, err := secretsStore()
	if err != nil {
		return secretsFail(stderr, err)
	}
	entries, err := st.List()
	if err != nil {
		return secretsFail(stderr, err)
	}
	for _, e := range entries {
		if code := say(stdout, "%s\t%s\n", e.Name, e.Fingerprint); code != 0 {
			return code
		}
	}
	return 0
}

// secretsMatrixRow is one matrix cell: a named secret referenced by a profile.
type secretsMatrixRow struct{ name, profile, fingerprint string }

// secretsMatrix prints `name<TAB>profile<TAB>fingerprint` for every named
// secret in every profile (GAP-OPS-04: id × profile). Values never appear.
func secretsMatrix(profiles []secretProfile, stdout, stderr io.Writer) int {
	var rows []secretsMatrixRow
	for _, p := range profiles {
		entries, err := p.st.List()
		if err != nil {
			return secretsFail(stderr, err)
		}
		for _, e := range entries {
			rows = append(rows, secretsMatrixRow{e.Name, secretProfileLabel(p.name), e.Fingerprint})
		}
	}
	slices.SortFunc(rows, func(a, b secretsMatrixRow) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		return strings.Compare(a.profile, b.profile)
	})
	for _, r := range rows {
		if code := say(stdout, "%s\t%s\t%s\n", r.name, r.profile, r.fingerprint); code != 0 {
			return code
		}
	}
	return 0
}

func secretsPut(cmd string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("setup secrets "+cmd, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var allProfiles *bool
	if cmd == "rotate" {
		allProfiles = flags.Bool("all-profiles", false, "rotate across every profile")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		say(stderr, "setup secrets %s: one name expected\n", cmd)
		return 2
	}
	name := flags.Arg(0)
	if !validSecretNameCLI(name) {
		say(stderr, "setup secrets %s: invalid name %q\n", cmd, cleanArg(name))
		return 2
	}
	if allProfiles != nil && *allProfiles {
		return secretsRotateAll(name, stdout, stderr)
	}
	value, err := readSecretValue(stdout)
	if err != nil {
		return secretsFail(stderr, err)
	}
	st, err := secretsStore()
	if err != nil {
		return secretsFail(stderr, err)
	}
	if cmd == "set" {
		err = st.Set(context.Background(), name, value)
	} else {
		err = st.Rotate(context.Background(), name, value)
	}
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			say(stderr, "setup secrets set: %q exists; use rotate\n", cleanArg(name))
			return 1
		}
		if errors.Is(err, secret.ErrKeyUnavailable) {
			say(stderr, "setup secrets: OS keychain unavailable; nothing written\n")
			return 1
		}
		return secretsFail(stderr, err)
	}
	if code := say(stdout, "%s %s\n", cmd, name); code != 0 {
		return code
	}
	return 0
}

func secretsStore() (*secret.Store, error) { return secretStoreForProfile(activeProfile()) }

func secretStoreForProfile(profile string) (*secret.Store, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(profile)
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	st, err := secret.New(secret.Options{ConfigDir: paths.ConfigDir, Profile: profile, Keyring: setupKeyring()})
	if err != nil {
		return nil, fmt.Errorf("secret store: %w", err)
	}
	return st, nil
}

// secretProfile is one profile's named-secret view ("" = default profile).
type secretProfile struct {
	name string
	st   secretNamed
}

// secretNamed is the subset of secret.Store the cross-profile commands use.
type secretNamed interface {
	List() ([]secret.NamedEntry, error)
	Get(context.Context, string) (string, error)
	Rotate(context.Context, string, string) error
}

// secretProfileStores returns the default profile and every on-disk profile,
// in `config profiles ls` order (default first, then sorted).
func secretProfileStores() ([]secretProfile, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	base, err := loader.Paths("")
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	names := []string{""}
	entries, err := os.ReadDir(filepath.Join(base.ConfigDir, "profiles"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("profiles: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "default" && validSecretNameCLI(e.Name()) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names[1:])
	out := make([]secretProfile, 0, len(names))
	for _, name := range names {
		st, err := secretStoreForProfile(name)
		if err != nil {
			return nil, err
		}
		out = append(out, secretProfile{name: name, st: st})
	}
	return out, nil
}

func secretProfileLabel(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

// secretsRotateAll rotates one named secret in every profile that references
// it (GAP-OPS-04). Profiles without the entry are reported, not failed.
func secretsRotateAll(name string, stdout, stderr io.Writer) int {
	value, err := readSecretValue(stdout)
	if err != nil {
		return secretsFail(stderr, err)
	}
	profiles, err := secretProfileStores()
	if err != nil {
		return secretsFail(stderr, err)
	}
	return rotateAllProfiles(context.Background(), profiles, name, value, stdout, stderr)
}

type secretRotateTarget struct {
	profile secretProfile
	old     string
	hasOld  bool
}

// rotateAllProfiles writes the new value profile by profile. The keyring is
// probed for every target before the first write, so an unavailable keychain
// leaves every profile unchanged; a failure during the write phase restores
// that profile's keyring entry when the old value is known.
func rotateAllProfiles(ctx context.Context, profiles []secretProfile, name, value string, stdout, stderr io.Writer) int {
	targets, missing, err := secretRotateTargets(profiles, name)
	if err != nil {
		return secretsFail(stderr, err)
	}
	for _, profile := range missing {
		if code := say(stdout, "skipped %s: no entry\n", secretProfileLabel(profile)); code != 0 {
			return code
		}
	}
	if len(targets) == 0 {
		say(stderr, "setup secrets rotate: no profile references %q\n", cleanArg(name))
		return 1
	}
	if err := probeSecretTargets(ctx, targets, name); err != nil {
		if errors.Is(err, secret.ErrKeyUnavailable) {
			say(stderr, "setup secrets: OS keychain unavailable; nothing written\n")
			return 1
		}
		return secretsFail(stderr, err)
	}
	return writeSecretTargets(ctx, targets, name, value, stdout, stderr)
}

// secretRotateTargets splits the profiles into those that reference name and
// those that do not (reported, never an error).
func secretRotateTargets(profiles []secretProfile, name string) (targets []secretRotateTarget, missing []string, err error) {
	for _, p := range profiles {
		entries, lerr := p.st.List()
		if lerr != nil {
			return nil, nil, fmt.Errorf("list %s: %w", secretProfileLabel(p.name), lerr)
		}
		if !slices.ContainsFunc(entries, func(e secret.NamedEntry) bool { return e.Name == name }) {
			missing = append(missing, p.name)
			continue
		}
		targets = append(targets, secretRotateTarget{profile: p})
	}
	return targets, missing, nil
}

// probeSecretTargets reads every target's current value before any write. An
// entry the keyring no longer holds yields no rollback value; an unavailable
// keyring aborts the whole command with nothing written.
func probeSecretTargets(ctx context.Context, targets []secretRotateTarget, name string) error {
	for i := range targets {
		old, err := targets[i].profile.st.Get(ctx, name)
		switch {
		case err == nil:
			targets[i].old, targets[i].hasOld = old, true
		case errors.Is(err, secret.ErrKeyMissing):
		default:
			return fmt.Errorf("probe %s: %w", secretProfileLabel(targets[i].profile.name), err)
		}
	}
	return nil
}

func writeSecretTargets(ctx context.Context, targets []secretRotateTarget, name, value string, stdout, stderr io.Writer) int {
	failed := 0
	for _, t := range targets {
		err := t.profile.st.Rotate(ctx, name, value)
		if err == nil {
			if code := say(stdout, "rotated %s\n", secretProfileLabel(t.profile.name)); code != 0 {
				return code
			}
			continue
		}
		failed++
		rollbackSecretTarget(ctx, t, name, stderr)
		say(stderr, "setup secrets rotate: profile %s: %s\n", secretProfileLabel(t.profile.name), cleanLine(err.Error()))
		if errors.Is(err, secret.ErrKeyUnavailable) {
			say(stderr, "setup secrets: OS keychain unavailable; stopping, remaining profiles unchanged\n")
			break
		}
	}
	if failed != 0 {
		return 1
	}
	return 0
}

// rollbackSecretTarget restores the old keyring value after a failed rotate so
// a failed index write never leaves a new value behind.
func rollbackSecretTarget(ctx context.Context, t secretRotateTarget, name string, stderr io.Writer) {
	if !t.hasOld {
		return
	}
	if err := t.profile.st.Rotate(ctx, name, t.old); err != nil {
		say(stderr, "setup secrets rotate: profile %s: rollback failed: %s\n", secretProfileLabel(t.profile.name), cleanLine(err.Error()))
	}
}

func secretsFail(stderr io.Writer, err error) int {
	say(stderr, "setup secrets: %s\n", cleanLine(err.Error()))
	return 1
}

func readSecretValue(stdout io.Writer) (string, error) {
	return readSecretNoEcho(stdout, func() (string, error) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		value := strings.TrimRight(line, "\r\n")
		if value == "" {
			return "", errors.New("empty secret value")
		}
		return value, nil
	})
}

// validSecretNameCLI mirrors the package pattern: lowercase, digits, . _ -.
func validSecretNameCLI(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
