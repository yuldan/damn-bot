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
	refs := flags.Bool("refs", false, "names only")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		say(stderr, "setup secrets ls: unexpected argument\n")
		return 2
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
		if *refs {
			if code := say(stdout, "%s\n", e.Name); code != 0 {
				return code
			}
			continue
		}
		if code := say(stdout, "%s\t%s\n", e.Name, e.Fingerprint); code != 0 {
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
		say(stderr, "setup secrets rotate: --all-profiles is not implemented in F3; named secrets are per profile\n")
		return 2
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

func secretsStore() (*secret.Store, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	st, err := secret.New(secret.Options{ConfigDir: paths.ConfigDir, Profile: activeProfile(), Keyring: setupKeyring()})
	if err != nil {
		return nil, fmt.Errorf("secret store: %w", err)
	}
	return st, nil
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
