//go:build !minimal && !skillstest

package main

import "github.com/damnbot/damnbot/internal/skills/hub"

// skillsHubTestSources is false in every release build: an env override can
// never point a source at a local server.
func skillsHubTestSources() bool { return false }

// skillsHubTestDial is false in every release build; the netx Tool edge keeps
// refusing loopback/IP literal dials.
func skillsHubTestDial() bool { return false }

// skillsHubSources returns nil so hub.New uses the pinned https bases.
func skillsHubSources() map[hub.Source]string { return nil }
