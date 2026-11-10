//go:build !minimal

package main

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/memoryproviders"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
)

// recallClient is the subset memoryAdapter uses; tests inject a fake.
type recallClient interface {
	Recall(ctx context.Context, query string) ([]memoryproviders.Snippet, error)
}

type memoryAdapter struct {
	client recallClient
}

// Recall maps provider hits to the assembler's raw snippets; the retrieval
// tier applies the <untrusted> wrapper and the shared 800-token cap.
func (m *memoryAdapter) Recall(ctx context.Context, query string) ([]assemble.Snippet, error) {
	hits, err := m.client.Recall(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("memory provider: %w", err)
	}
	out := make([]assemble.Snippet, 0, len(hits))
	for _, h := range hits {
		out = append(out, assemble.Snippet{Source: h.Source, Text: h.Text})
	}
	return out, nil
}

// newExternalMemory builds the optional provider from [modules.memory]. The
// default-off path reads no credential and creates no transport.
func newExternalMemory(ctx context.Context, cfg *config.Config, prov loop.Provider, modelRef string) (externalMemory, error) {
	section := cfg.Modules.Memory
	if section.Provider == "" || section.Provider == "off" {
		return nil, nil
	}
	if section.Provider != memoryproviders.SourceHoncho {
		return nil, fmt.Errorf("%w: %q", memoryproviders.ErrUnknownProvider, section.Provider)
	}
	if !section.Honcho.Enabled {
		return nil, nil
	}
	token, err := memoryToken(ctx, section.Honcho.TokenSecret)
	if err != nil {
		return nil, err
	}
	settings := memoryproviders.Settings{
		HTTP: netx.New(netx.Config{
			Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background,
			Secrets: []string{token},
		}).Client("memory-honcho", netx.Tool),
		BaseURL: section.Honcho.BaseURL, Token: token,
		Workspace: section.Honcho.Workspace, SessionID: section.Honcho.SessionID,
		PeerID: section.Honcho.PeerID, KnownSecrets: []string{token},
	}
	if section.QueryRewrite {
		settings.Rewriter = memoryproviders.NewLLMRewriter(providerCompleter{prov: prov, model: modelRef})
	}
	client, err := memoryproviders.Open(section.Provider, &settings)
	if err != nil {
		return nil, fmt.Errorf("memory provider: %w", err)
	}
	return &memoryAdapter{client: client}, nil
}

func memoryToken(ctx context.Context, name string) (string, error) {
	store, err := secretsStore()
	if err != nil {
		return "", fmt.Errorf("memory provider keychain: %w", err)
	}
	token, err := store.Get(ctx, name)
	if err != nil || token == "" {
		return "", fmt.Errorf("%w: keychain entry %q", memoryproviders.ErrUnavailable, name)
	}
	return token, nil
}

// providerCompleter is the auxiliary English rewrite call (MEM-009). It shares
// the run provider and is only reachable when query_rewrite is enabled.
type providerCompleter struct {
	prov  loop.Provider
	model string
}

func (p providerCompleter) Complete(ctx context.Context, system, user string) (string, error) {
	reply, err := purposeStream(ctx, p.prov, provider.PurposeTurn, &loop.Request{
		Model: p.model, MaxTokens: 128,
		Messages: []loop.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
	})
	if err != nil {
		return "", err
	}
	return reply.Content, nil
}
