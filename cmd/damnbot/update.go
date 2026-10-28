package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/update"
)

const updateAvailableExit = 10

func updateUsage(w io.Writer) {
	sessionsPrintf(w, "%s", `usage: damnbot update [--check] [--channel stable|beta] [--config FILE]
       damnbot update --rollback [--data]
--check: signed target only (exit 0/10/1/2). install verifies + swaps. rollback uses pending.json. Embedded key (env only for builds without one).
`)
}

func runUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("damnbot update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "show the newest release without installing")
	channel := fs.String("channel", "", "stable|beta (default from [update])")
	rollback := fs.Bool("rollback", false, "restore the previous binary from pending.json")
	withData := fs.Bool("data", false, "with --rollback: restore the paired database backup")
	cfgPath := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch {
	case fs.NArg() != 0:
		say(stderr, "damnbot update: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	case *withData && !*rollback:
		say(stderr, "damnbot update: --data requires --rollback\n")
		return 2
	}
	loader, lerr := config.New(config.OS())
	if lerr != nil {
		say(stderr, "damnbot update: config deps: %v\n", lerr)
		return 2
	}
	paths, perr := loader.Paths(activeProfile())
	if perr != nil {
		say(stderr, "damnbot update: config paths: %v\n", perr)
		return 2
	}
	if *rollback {
		return runRollback(paths.DataDir, *withData, stdout, stderr)
	}
	cfg, cerr := loadRunConfig(loader, *cfgPath)
	if cerr != nil {
		say(stderr, "damnbot update: %v\n", cerr)
		return 2
	}
	uc := cfg.Update
	if *channel != "" {
		uc.Channel = *channel
	}
	if verr := uc.Validate(); verr != nil {
		say(stderr, "damnbot update: %v\n", verr)
		return 2
	}
	keyText := update.EmbeddedPublicKey()
	if keyText == "" {
		keyText = os.Getenv("DAMNBOT_RELEASE_PUBKEY")
	}
	pub, kerr := update.ParsePublicKey(keyText)
	if kerr != nil {
		say(stderr, "damnbot update: %v\n", kerr)
		return 2
	}
	client := netx.New(netx.Config{Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background}).Client("update", netx.Background)
	if *check {
		return runCheck(client, uc, pub, stdout, stderr)
	}
	return runInstall(client, uc, pub, paths.DataDir, stdout, stderr)
}

func runCheck(client *netx.Client, uc update.Config, pub ed25519.PublicKey, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target, changelog, available, err := update.Check(ctx, client, uc.FeedURL, uc.Channel, version, pub)
	if err != nil {
		say(stderr, "damnbot update: %v\n", err)
		return 1
	}
	say(stdout, "installed %s\nchannel %s\n", version, uc.Channel)
	if !available {
		say(stdout, "up to date (%s)\n", target)
		return 0
	}
	say(stdout, "target %s\n", target)
	if changelog != "" {
		say(stdout, "changelog:\n%s\n", cleanBlock(changelog))
	}
	return updateAvailableExit
}

func runInstall(client *netx.Client, uc update.Config, pub ed25519.PublicKey, dataDir string, stdout, stderr io.Writer) int {
	bin, err := os.Executable()
	if err != nil {
		say(stderr, "damnbot update: executable: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	target, err := update.Install(ctx, &update.InstallOptions{
		Client: client, FeedURL: uc.FeedURL, Channel: uc.Channel, Current: version,
		PublicKey: pub, DataDir: dataDir, BinaryPath: bin,
	})
	if err != nil {
		say(stderr, "damnbot update: %v\n", err)
		return updateErrorCode(err)
	}
	say(stdout, "installed %s (previous %s)\n", target, version)
	return 0
}

func runRollback(dataDir string, withData bool, stdout, stderr io.Writer) int {
	if err := update.Rollback(dataDir, withData); err != nil {
		say(stderr, "damnbot update: %v\n", err)
		return updateErrorCode(err)
	}
	say(stdout, "rolled back (data=%t)\n", withData)
	return 0
}

func updateErrorCode(err error) int {
	if errors.Is(err, update.ErrLocked) || errors.Is(err, update.ErrNoPending) {
		return 2
	}
	return 1
}
