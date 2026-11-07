//go:build !minimal

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
)

// slackUsage documents the manifest helper (CLI-017).
func slackUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot slack manifest [--name NAME] [--request-url https://...]\n")
	sessionsPrintf(w, "prints a deterministic Slack app manifest (scopes/events); it never carries a credential\n")
}

// runSlack implements `damnbot slack` (CLI-017): the helper emits the manifest
// an operator pastes into Slack, so the app is configured from the same source
// as the adapter instead of from a screenshot.
func runSlack(args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) > 0 && isHelp(args[0]):
		slackUsage(stdout)
		return 0
	case len(args) == 0 || args[0] != "manifest":
		slackUsage(stderr)
		return 2
	}
	return slackManifest(args[1:], stdout, stderr)
}

func slackManifest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("slack manifest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "damnbot", "Slack app display name")
	requestURL := fs.String("request-url", "", "Events API request URL (https; omitted enables socket mode)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "slack manifest: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	if *requestURL != "" && !httpsURL(*requestURL) {
		say(stderr, "slack manifest: --request-url must be an https URL without credentials\n")
		return 2
	}
	data, err := slackManifestJSON(*name, *requestURL)
	if err != nil {
		say(stderr, "slack manifest: %s\n", cleanLine(err.Error()))
		return 1
	}
	if _, werr := stdout.Write(append(data, '\n')); werr != nil {
		return 1
	}
	return 0
}

// slackManifestJSON builds the manifest. encoding/json sorts map keys, so the
// output is stable for a fixed name and request URL.
func slackManifestJSON(name, requestURL string) ([]byte, error) {
	// Both lists are sorted so the manifest is byte-identical between runs.
	scopes := []string{
		"app_mentions:read", "channels:history", "chat:write", "groups:history",
		"im:history", "im:read", "reactions:read", "users:read",
	}
	botEvents := []string{"app_mention", "message.channels", "message.groups", "message.im"}
	events := map[string]any{"bot_events": botEvents}
	if requestURL != "" {
		events["request_url"] = requestURL
	}
	manifest := map[string]any{
		"display_information": map[string]any{"name": name},
		"features": map[string]any{
			"bot_user": map[string]any{"display_name": name, "always_online": false},
		},
		"oauth_config": map[string]any{"scopes": map[string]any{"bot": scopes}},
		"settings": map[string]any{
			"event_subscriptions": events,
			"interactivity":       map[string]any{"is_enabled": true},
			"socket_mode_enabled": requestURL == "",
		},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("slack manifest: encode: %w", err)
	}
	return data, nil
}

// httpsURL accepts only a credential-free https URL, the same rule the webhook
// store applies to outbound targets.
func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != "" && u.User == nil
}
