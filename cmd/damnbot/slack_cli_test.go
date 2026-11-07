//go:build !minimal

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestF7CLI017SlackManifest proves the Slack helper (CLI-017): the manifest is
// deterministic JSON with scopes and bot events, and it never carries a token
// or any other credential.
func TestF7CLI017SlackManifest(t *testing.T) {
	code, first, stderr := runArgs(t, "slack", "manifest")
	if code != 0 {
		t.Fatalf("manifest = %d (%s)", code, stderr)
	}
	code, second, stderr := runArgs(t, "slack", "manifest")
	if code != 0 {
		t.Fatalf("second manifest = %d (%s)", code, stderr)
	}
	if first != second {
		t.Fatal("manifest is not deterministic")
	}
	if strings.Contains(strings.ToLower(first), "token") || strings.Contains(first, "xoxb-") {
		t.Fatalf("manifest carries a credential: %s", first)
	}
	manifest := decodeSlackManifest(t, first)
	if !hasString(manifest, "oauth_config", "scopes", "bot", "chat:write") {
		t.Errorf("manifest misses the chat:write scope: %s", first)
	}
	if !hasString(manifest, "settings", "event_subscriptions", "bot_events", "app_mention") {
		t.Errorf("manifest misses the app_mention event: %s", first)
	}

	checkSlackManifestHTTP(t)
	checkSlackManifestRejectsHTTP(t)
}

// decodeSlackManifest parses the helper output as a generic JSON object.
func decodeSlackManifest(t *testing.T, out string) map[string]any {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal([]byte(out), &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, out)
	}
	return manifest
}

// hasString reports whether the nested path holds want in a string list.
func hasString(manifest map[string]any, path ...string) bool {
	var node any = manifest
	for _, key := range path[:len(path)-1] {
		obj, ok := node.(map[string]any)
		if !ok {
			return false
		}
		node = obj[key]
	}
	list, ok := node.([]any)
	if !ok {
		return false
	}
	want := path[len(path)-1]
	for _, item := range list {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}

func checkSlackManifestHTTP(t *testing.T) {
	t.Helper()
	code, out, stderr := runArgs(t, "slack", "manifest", "--request-url", "https://hooks.example.test/slack")
	if code != 0 {
		t.Fatalf("manifest with request URL = %d (%s)", code, stderr)
	}
	manifest := decodeSlackManifest(t, out)
	settings, ok := manifest["settings"].(map[string]any)
	if !ok || settings["socket_mode_enabled"] != false {
		t.Fatalf("socket mode is not disabled for the HTTP surface: %s", out)
	}
	events, ok := settings["event_subscriptions"].(map[string]any)
	if !ok || events["request_url"] != "https://hooks.example.test/slack" {
		t.Fatalf("request_url missing: %s", out)
	}
}

func checkSlackManifestRejectsHTTP(t *testing.T) {
	t.Helper()
	code, _, stderr := runArgs(t, "slack", "manifest", "--request-url", "http://insecure.example.test/slack")
	if code != 2 || !strings.Contains(stderr, "https") {
		t.Fatalf("plain http request URL = %d (%s)", code, stderr)
	}
}
