//go:build !minimal

package config

import (
	"net"
	"net/url"
)

// ModulesConnectors is [modules.connectors] (SPEC §9.9): the F8 connector
// module. Every connector owns its own sub-section, added by the slice that
// implements it; this core only carries the module switch and hard limits.
type ModulesConnectors struct {
	Enabled          bool                    `toml:"enabled"`
	TimeoutS         int                     `toml:"timeout_s"`
	MaxRetries       int                     `toml:"max_retries"`
	MaxResponseBytes int64                   `toml:"max_response_bytes"`
	Graph            ModulesConnectorGraph   `toml:"graph"`
	Teams            ModulesConnectorTeams   `toml:"teams"`
	Feishu           ModulesConnectorFeishu  `toml:"feishu"`
	Meet             ModulesConnectorMeet    `toml:"meet"`
	XSearch          ModulesConnectorXSearch `toml:"x_search"`
	Discord          ModulesConnectorDiscord `toml:"discord"`
	OSV              ModulesConnectorOSV     `toml:"osv"`
}

// ModulesConnectorGraph is [modules.connectors.graph].
type ModulesConnectorGraph struct {
	Enabled         bool   `toml:"enabled"`
	TenantID        string `toml:"tenant_id"`
	ClientID        string `toml:"client_id"`
	ClientSecretEnv string `toml:"client_secret_env"`
	RefreshTokenEnv string `toml:"refresh_token_env"`
	TokenURL        string `toml:"token_url"`
	BaseURL         string `toml:"base_url"`
}

// ModulesConnectorTeams is [modules.connectors.teams].
type ModulesConnectorTeams struct {
	Enabled         bool   `toml:"enabled"`
	NotificationURL string `toml:"notification_url"`
}

// ModulesConnectorFeishu is [modules.connectors.feishu].
type ModulesConnectorFeishu struct {
	Enabled      bool   `toml:"enabled"`
	AppID        string `toml:"app_id"`
	AppSecretEnv string `toml:"app_secret_env"`
	BaseURL      string `toml:"base_url"`
}

// ModulesConnectorMeet is [modules.connectors.meet] (Q9).
type ModulesConnectorMeet struct {
	Enabled             bool   `toml:"enabled"`
	NodeURL             string `toml:"node_url"`
	NodeTokenEnv        string `toml:"node_token_env"`
	ConsentText         string `toml:"consent_text"`
	ConsentAnnouncement *bool  `toml:"consent_announcement"`
	AckConsentAt        string `toml:"ack_consent_responsibility_at"`
}

// ModulesConnectorXSearch is [modules.connectors.x_search].
type ModulesConnectorXSearch struct {
	Enabled bool   `toml:"enabled"`
	KeyEnv  string `toml:"key_env"`
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
}

// ModulesConnectorDiscord is [modules.connectors.discord].
type ModulesConnectorDiscord struct {
	Enabled     bool   `toml:"enabled"`
	BotTokenEnv string `toml:"bot_token_env"`
	BaseURL     string `toml:"base_url"`
	Admin       bool   `toml:"admin"`
}

// ModulesConnectorOSV is [modules.connectors.osv].
type ModulesConnectorOSV struct {
	Enabled bool   `toml:"enabled"`
	BaseURL string `toml:"base_url"`
}

// ModulesMemory is [modules.memory] (MEM-005/008/009, ADR-338). The section
// is default off: an empty provider means no external call and no credential
// read. ByteRover and RetainDB never decode into a working provider (NP-21).
type ModulesMemory struct {
	Provider      string              `toml:"provider"`
	QueryRewrite  bool                `toml:"query_rewrite"`
	ContextEngine string              `toml:"context_engine"`
	Honcho        ModulesMemoryHoncho `toml:"honcho"`
}

// ModulesMemoryHoncho is [modules.memory.honcho]. TokenSecret is a keychain
// entry name, never the token.
type ModulesMemoryHoncho struct {
	Enabled     bool   `toml:"enabled"`
	BaseURL     string `toml:"base_url"`
	TokenSecret string `toml:"token_secret"`
	Workspace   string `toml:"workspace"`
	SessionID   string `toml:"session_id"`
	PeerID      string `toml:"peer_id"`
}

// validateMemory enforces the closed provider table and the default-off
// combinations. It never resolves the credential.
func (m *Modules) validateMemory() error {
	mem := m.Memory
	switch mem.Provider {
	case "", "off":
		if mem.Honcho.Enabled {
			return invalid("modules.memory.honcho.enabled", "requires provider = \"honcho\"")
		}
	case "honcho":
		if !mem.Honcho.Enabled {
			return invalid("modules.memory.honcho.enabled", "must be true when provider = \"honcho\"")
		}
	case "byterover", "retaindb":
		return invalid("modules.memory.provider", "%q is not ported: upstream removal 2026-10-15 (NP-21, ADR-033)", mem.Provider)
	default:
		return invalid("modules.memory.provider", "unknown provider %q", mem.Provider)
	}
	switch mem.ContextEngine {
	case "", "compressor":
	default:
		return invalid("modules.memory.context_engine", "unknown engine %q (default off)", mem.ContextEngine)
	}
	if !mem.Honcho.Enabled {
		return nil
	}
	if mem.Honcho.TokenSecret == "" {
		return invalid("modules.memory.honcho.token_secret", "must name a keychain entry")
	}
	if mem.Honcho.BaseURL != "" && !validMemoryBaseURL(mem.Honcho.BaseURL) {
		return invalid("modules.memory.honcho.base_url", "must be https or a loopback http URL")
	}
	return nil
}

func validMemoryBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateF8 keeps the module-level limits inside the framework bounds.
func (m *Modules) validateF8() error {
	c := m.Connectors
	if c.TimeoutS < 0 || c.TimeoutS > 120 {
		return invalid("modules.connectors.timeout_s", "must be 1..120")
	}
	if c.MaxRetries < 0 || c.MaxRetries > 3 {
		return invalid("modules.connectors.max_retries", "must be 0..3")
	}
	if c.MaxResponseBytes < 0 || c.MaxResponseBytes > 8<<20 {
		return invalid("modules.connectors.max_response_bytes", "must be 1..8388608")
	}
	return m.validateMemory()
}
