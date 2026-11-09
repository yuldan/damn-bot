//go:build !minimal && skillstest

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/skills/hub"
)

// skillsHubTestSources enables the loopback-only source override. Only the
// `skillstest` e2e build sets this tag.
func skillsHubTestSources() bool { return true }

// skillsHubTestDial lets the netx Tool edge dial the fake registry.
func skillsHubTestDial() bool { return true }

// skillsHubSources reads DAMNBOT_SKILLS_HUB_<SOURCE>_URL. hub.New still
// validates every value as loopback-only because AllowTestSources is true.
func skillsHubSources() map[hub.Source]string {
	out := make(map[hub.Source]string, len(hub.Sources()))
	for _, src := range hub.Sources() {
		key := "DAMNBOT_SKILLS_HUB_" + strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(string(src))) + "_URL"
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			out[src] = v
		}
	}
	return out
}

// skillsHubIndexKey reads the e2e/operator ed25519 public key.
func skillsHubIndexKey() ed25519.PublicKey {
	raw := strings.TrimSpace(config.OS().Getenv("DAMNBOT_SKILLS_HUB_KEY"))
	if raw == "" {
		return nil
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(key)
}
