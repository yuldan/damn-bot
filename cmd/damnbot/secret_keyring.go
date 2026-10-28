//go:build !secrettest

package main

import "github.com/damnbot/damnbot/internal/secret"

// setupKeyring returns nil in production: secret.New uses the OS keychain.
func setupKeyring() secret.Keyring { return nil }
