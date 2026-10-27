package main

import (
	"context"
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
	sessionsPrintf(w, "usage: damnbot update --check [--channel stable|beta] [--config FILE]\n--check prints the signed target; no install. Exit 0/10/1/2; key embedded (env only for builds without one).\n")
}

// runUpdate implements SPEC §10 update (ADR-216); install/rollback are D2b.
func runUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("damnbot update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "show the newest release without installing")
	channel := fs.String("channel", "", "stable|beta (default from [update])")
	cfgPath := fs.String("config", "", "config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "damnbot update: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	if !*check {
		say(stderr, "damnbot update: installation and rollback are not available in this build; use --check\n")
		return 2
	}
	loader, err := config.New(config.OS())
	if err != nil {
		say(stderr, "damnbot update: config deps: %v\n", err)
		return 2
	}
	cfg, err := loadRunConfig(loader, *cfgPath)
	if err != nil {
		say(stderr, "damnbot update: %v\n", err)
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
	pub, err := update.ParsePublicKey(keyText)
	if err != nil {
		say(stderr, "damnbot update: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target, changelog, available, err := update.Check(ctx,
		netx.New(netx.Config{}).Client("update", netx.Background),
		uc.FeedURL, uc.Channel, version, pub)
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
