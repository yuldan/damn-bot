//go:build !minimal

package main

import (
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/connectors"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
)

const f8ConnectorsID = "connectors"

func connectorsDescriptor() modules.Descriptor {
	return modules.Descriptor{
		ID:        f8ConnectorsID,
		Group:     "connectors",
		Catalog:   "- connectors: graph/teams/feishu/meet/x/discord/osv",
		Tools:     connectorToolDescriptors(),
		ConfigKey: "modules.connectors",
		Factory: func(_ host.Host, cfg host.Config) (modules.Module, error) {
			store, err := connectors.OpenConnections(f8HostedPath())
			if err != nil {
				return nil, fmt.Errorf("hosted accounts: %w", err)
			}
			return connectors.NewFromConfig(cfg.Raw, connectors.Deps{
				Secrets: func(name string) (string, error) { return config.LookupSecret(name), nil },
				Manage:  &connectors.ManageDeps{Accounts: store},
			})
		},
	}
}

func connectorToolDescriptors() []modules.ToolDescriptor {
	tools := connectors.Tools()
	out := make([]modules.ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		out = append(out, modules.ToolDescriptor{
			Name: tool.Name, Description: tool.Description, Schema: tool.Schema,
			Effect: tool.Effect, Tags: tool.Tags,
		})
	}
	return out
}

func rawSection(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("connectors section: %w", err)
	}
	return raw, nil
}

// connectorsSection is the JSON form of [modules.connectors] handed to the
// module; credentials stay env names.
func connectorsSection(cfg *config.Config) (connectors.Section, error) {
	c := cfg.Modules.Connectors
	section := connectors.Section{
		Enabled: c.Enabled, Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background,
		TimeoutS: c.TimeoutS, MaxRetries: c.MaxRetries, MaxResponseBytes: c.MaxResponseBytes,
	}
	fields := []struct {
		dst *json.RawMessage
		src any
	}{
		{&section.Graph, c.Graph},
		{&section.Teams, c.Teams},
		{&section.Feishu, c.Feishu},
		{&section.Meet, c.Meet},
		{&section.XSearch, c.XSearch},
		{&section.Discord, c.Discord},
		{&section.OSV, c.OSV},
	}
	for _, f := range fields {
		raw, err := rawSection(f.src)
		if err != nil {
			return connectors.Section{}, err
		}
		*f.dst = raw
	}
	return section, nil
}

func connectorsEnabled(cfg *config.Config) bool { return cfg.Modules.Connectors.Enabled }

// f8HostedPath is the shared hosted-account store of the module and the CLI.
func f8HostedPath() string {
	dir, err := f8Dir("")
	if err != nil {
		return ""
	}
	return f8StorePath(dir, "hosted.json")
}
