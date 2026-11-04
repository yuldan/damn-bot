//go:build !minimal

package browser

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Section is the JSON form of [modules.browser] handed to the module through
// host.Config.Raw. It is the module's own schema: the core copies these keys
// without interpreting them. The field order matches ModuleConfig so the
// conversion stays exhaustive when a key is added.
type Section struct {
	Enabled       bool     `json:"enabled"`
	Engine        string   `json:"engine"`
	CDPURL        string   `json:"cdp_url"`
	CamofoxURL    string   `json:"camofox_url"`
	LightpandaBin string   `json:"lightpanda_bin"`
	CloudURL      string   `json:"cloud_url"`
	AllowOrigins  []string `json:"allow_origins"`
	LocalProfile  bool     `json:"local_profile"`
	DialogsAllow  []string `json:"dialog_allow"`
	Offline       bool     `json:"offline"`
	NoBackground  bool     `json:"no_background"`
}

// NewFromConfig decodes one [modules.browser] section and builds the module.
// The section is strict: an unknown key is a wiring error rather than a
// silently ignored setting, and validation runs even when disabled.
func NewFromConfig(raw json.RawMessage, deps Deps) (*Module, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: empty [modules.browser] section", errBadConfig)
	}
	var section Section
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&section); err != nil {
		return nil, fmt.Errorf("%w: %w", errBadConfig, err)
	}
	cfg := ModuleConfig(section)
	return New(&cfg, deps)
}
