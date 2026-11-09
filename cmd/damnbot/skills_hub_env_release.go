//go:build !minimal && !skillstest

package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/skills/hub"
)

// skillsHubTestSources is false in every release build: an env override can
// never point a source at a local server.
func skillsHubTestSources() bool { return false }

// skillsHubTestDial is false in every release build; the netx Tool edge keeps
// refusing loopback/IP literal dials.
func skillsHubTestDial() bool { return false }

// skillsHubSources returns nil so hub.New uses the pinned https bases.
func skillsHubSources() map[hub.Source]string { return nil }

// skillsHubIndexKey reads the operator-provisioned ed25519 public key; an
// empty or malformed value fails closed at Index time.
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
