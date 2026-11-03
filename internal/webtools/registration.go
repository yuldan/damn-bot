//go:build !minimal

package webtools

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/netx"
)

// Section is the JSON form of [modules.web] that the composition root passes
// through host.Config.Raw. It is the module's own schema (SPEC §3.2.1): the
// core never decodes these keys, it only copies the section.
type Section struct {
	Enabled      bool     `json:"enabled"`
	Engines      []string `json:"engines"`
	Blocklist    []string `json:"blocklist"`
	Offline      bool     `json:"offline"`
	NoBackground bool     `json:"no_background"`
	UserAgent    string   `json:"user_agent"`
	MaxOutput    int      `json:"max_output"`
}

// NewFromConfig decodes one [modules.web] section and builds the module with
// its own tool-egress client. It is the composition-root seam of the module:
// the client is created only for an enabled module, so a disabled descriptor
// starts nothing (P16). The section is strict; an unknown key is a wiring
// error rather than a silently ignored setting.
func NewFromConfig(raw json.RawMessage) (*Module, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: empty [modules.web] section", errBadConfig)
	}
	var section Section
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&section); err != nil {
		return nil, fmt.Errorf("%w: %w", errBadConfig, err)
	}
	cfg := &Config{
		Enabled:   section.Enabled,
		Offline:   section.Offline,
		Engines:   section.Engines,
		UserAgent: section.UserAgent,
		MaxOutput: section.MaxOutput,
		blocklist: section.Blocklist,
	}
	if !cfg.Enabled {
		return New(nil, cfg)
	}
	if cfg.UserAgent == "" {
		return nil, fmt.Errorf("%w: user_agent is required when the module is enabled", errBadConfig)
	}
	client := netx.New(netx.Config{Offline: section.Offline, NoBackground: section.NoBackground, TestDial: testEgress()}).
		Client("web", netx.Tool)
	return New(client, cfg)
}
