//go:build !minimal

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gwplat/webhook"
	"github.com/damnbot/damnbot/internal/secret"
)

// webhookSecretGet is the named-secret read of `damnbot webhook test`;
// production reads the OS keychain.
func webhookSecretGet(ctx context.Context, configDir, profile, name string) (string, error) {
	store, err := secret.New(secret.Options{ConfigDir: configDir, Profile: profile})
	if err != nil {
		return "", fmt.Errorf("secret store: %w", err)
	}
	return store.Get(ctx, name)
}

func webhookUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot webhook <add|list|remove|test> [flags]\n\n")
	sessionsPrintf(w, "  add     --url <https> --event <name> [--event <name>...] [--secret <ref>] [--provider generic|msgraph] [--expires <RFC3339>]\n")
	sessionsPrintf(w, "  list    [--data-dir <dir>]\n")
	sessionsPrintf(w, "  remove  <id> [--data-dir <dir>]\n")
	sessionsPrintf(w, "  test    <id> [--data-dir <dir>]   send exactly one signed test event\n")
}

func runWebhook(args []string, stdout, stderr io.Writer) int {
	return runWebhookWith(args, stdout, stderr, webhookSecretGet)
}

// runWebhookWith is runWebhook with an injected secret reader (tests never
// touch the OS keychain).
func runWebhookWith(args []string, stdout, stderr io.Writer,
	secretGet func(context.Context, string, string, string) (string, error),
) int {
	if len(args) == 0 {
		webhookUsage(stderr)
		return 2
	}
	switch args[0] {
	case "add", "subscribe":
		return webhookAdd(args[1:], stdout, stderr)
	case "list", "ls":
		return webhookList(args[1:], stdout, stderr)
	case "remove", "rm":
		return webhookRemove(args[1:], stdout, stderr)
	case "test":
		return webhookTest(args[1:], stdout, stderr, secretGet)
	case "help", "-h", "--help":
		webhookUsage(stdout)
		return 0
	}
	sessionsPrintf(stderr, "webhook: unknown subcommand %q\n", cleanArg(args[0]))
	webhookUsage(stderr)
	return 2
}

type webhookEnv struct {
	store  *webhook.Store
	config string
}

func openWebhookEnv(dataDir string) (*webhookEnv, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	dir := dataDir
	if dir == "" {
		dir = paths.DataDir
	}
	store, err := webhook.OpenStore(filepath.Join(dir, "webhook", "subscriptions.json"), nil)
	if err != nil {
		return nil, err
	}
	return &webhookEnv{store: store, config: paths.ConfigDir}, nil
}

type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func webhookAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("webhook add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	target := fs.String("url", "", "target https URL (loopback http is allowed)")
	provider := fs.String("provider", "generic", "generic or msgraph")
	secretRef := fs.String("secret", "webhook_secret", "named secret reference")
	expires := fs.String("expires", "", "expiry (RFC3339, msgraph)")
	var events repeatedFlag
	fs.Var(&events, "event", "event name (repeatable)")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 0 {
		webhookUsage(stderr)
		return 2
	}
	sub := webhook.Subscription{
		Provider: *provider, URL: *target, Events: events,
		SecretRef: *secretRef, ExpiresAt: time.Time{},
	}
	if *expires != "" {
		parsed, perr := time.Parse(time.RFC3339, *expires)
		if perr != nil {
			sessionsPrintf(stderr, "webhook add: invalid --expires: %s\n", cleanLine(perr.Error()))
			return 2
		}
		sub.ExpiresAt = parsed
	}
	env, err := openWebhookEnv(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "webhook add: %s\n", cleanLine(err.Error()))
		return 1
	}
	stored, created, err := env.store.Add(sub)
	if err != nil {
		sessionsPrintf(stderr, "webhook add: %s\n", cleanLine(err.Error()))
		return 1
	}
	verb := "exists"
	if created {
		verb = "added"
	}
	sessionsPrintf(stdout, "%s %s %s %s events=%s secret=%s\n", verb, stored.ID, stored.Provider,
		stored.URL, strings.Join(stored.Events, ","), stored.SecretRef)
	return 0
}

func webhookList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("webhook list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 0 {
		webhookUsage(stderr)
		return 2
	}
	env, err := openWebhookEnv(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "webhook list: %s\n", cleanLine(err.Error()))
		return 1
	}
	subs := env.store.List()
	if len(subs) == 0 {
		sessionsPrintf(stdout, "no webhook subscriptions\n")
		return 0
	}
	for _, sub := range subs {
		expiry := "-"
		if !sub.ExpiresAt.IsZero() {
			expiry = sub.ExpiresAt.UTC().Format(time.RFC3339)
		}
		sessionsPrintf(stdout, "%s %s %s events=%s secret=%s expires=%s\n", sub.ID, sub.Provider,
			sub.URL, strings.Join(sub.Events, ","), sub.SecretRef, expiry)
	}
	return 0
}

func webhookRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("webhook remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		webhookUsage(stderr)
		return 2
	}
	env, err := openWebhookEnv(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "webhook remove: %s\n", cleanLine(err.Error()))
		return 1
	}
	if err := env.store.Remove(pos[0]); err != nil {
		sessionsPrintf(stderr, "webhook remove: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "removed %s\n", cleanArg(pos[0]))
	return 0
}

func webhookTest(args []string, stdout, stderr io.Writer,
	secretGet func(context.Context, string, string, string) (string, error),
) int {
	fs := flag.NewFlagSet("webhook test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 {
		webhookUsage(stderr)
		return 2
	}
	env, err := openWebhookEnv(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "webhook test: %s\n", cleanLine(err.Error()))
		return 1
	}
	sub, ok := env.store.Get(pos[0])
	if !ok {
		sessionsPrintf(stderr, "webhook test: %s\n", cleanLine(webhook.ErrNotFound.Error()+": "+pos[0]))
		return 1
	}
	value, err := secretGet(context.Background(), env.config, activeProfile(), sub.SecretRef)
	if err != nil || strings.TrimSpace(value) == "" {
		sessionsPrintf(stderr, "webhook test: secret %q is unavailable\n", cleanArg(sub.SecretRef))
		return 1
	}
	sender := &webhook.Sender{Client: &http.Client{Timeout: 10 * time.Second}}
	receipt, err := sender.Send(context.Background(), sub, []byte(value), "test")
	if err != nil {
		sessionsPrintf(stderr, "webhook test: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "sent 1 test event id=%s status=%d attempts=%d url=%s\n",
		receipt.ID, receipt.Status, receipt.Attempts, sub.URL)
	return 0
}
