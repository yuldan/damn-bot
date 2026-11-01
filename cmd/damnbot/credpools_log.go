//go:build !minimal

package main

import (
	"context"
	"log/slog"

	"github.com/damnbot/damnbot/internal/provider"
)

// configureCredentialLog wires the credential_rotated event of the full
// build: identities and reason only, never a key or token (P7).
func configureCredentialLog(ctx context.Context, cfg *provider.Config) {
	cfg.OnCredential = func(e provider.CredentialEvent) {
		slog.WarnContext(ctx, "credential rotated", "provider", e.Provider, "from", e.From, "to", e.To,
			"reason", e.Reason, "cooldown", e.Cooldown, "attempt", e.Attempt)
	}
}
