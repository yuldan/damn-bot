package computeruse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// Section is the JSON form of `[modules.computer_use]` handed to the module
// through host.Config.Raw (ADR-309, ADR-314). The core copies these keys
// without interpreting them; the module owns the validation.
type Section struct {
	Enabled            bool     `json:"enabled"`
	Backend            string   `json:"backend"`
	Endpoint           string   `json:"endpoint"`
	Command            []string `json:"command"`
	Env                []string `json:"env"`
	ActionTimeoutMS    int      `json:"action_timeout_ms"`
	MaxScreenshotBytes int      `json:"max_screenshot_bytes"`
}

// Config normalises the section into the module's runtime configuration.
func (s *Section) Config() Config {
	return Config{
		Backend:            s.Backend,
		Endpoint:           s.Endpoint,
		Command:            s.Command,
		Env:                s.Env,
		Timeout:            time.Duration(s.ActionTimeoutMS) * time.Millisecond,
		MaxScreenshotBytes: s.MaxScreenshotBytes,
	}
}

// DepsFromHost extracts the core-owned seam the module needs. A nil host gets
// fail-closed defaults; the module then fails closed at the first action.
func DepsFromHost(h host.Host) Deps {
	caps := host.CapabilitiesOf(h)
	deps := Deps{Approvals: caps.Approvals, Secrets: caps.Secrets}
	if h != nil {
		deps.Spawn = h.Spawn
	}
	return deps
}

// NewFromConfig decodes one strict [modules.computer_use] section and builds
// the module. An unknown key is a wiring error, never a silent no-op; the
// section is validated even when disabled.
func NewFromConfig(raw json.RawMessage, deps Deps) (*Module, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("%w: empty [modules.computer_use] section", ErrInvalid)
	}
	var s Section
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	cfg := s.Config()
	m, err := New(&cfg, deps)
	if err != nil {
		return nil, err
	}
	m.enabled = s.Enabled
	return m, nil
}

// ToolSpec is one deferred catalog entry of the computer-use module.
type ToolSpec struct {
	Name, Description, Effect string
	Tags                      []string
	Schema                    json.RawMessage
}

// Tools returns the module's single deferred tool. The action vocabulary is
// the closed set of ADR-314; `kind` decides which fields are used.
func Tools() []ToolSpec {
	return []ToolSpec{{
		Name:        ToolName,
		Description: "Control the desktop through the external cua backend: screenshot, move, click, type, key, scroll or wait",
		Effect:      string(host.EffectNonIdempotent),
		Tags:        []string{"computer", "desktop", "screen", "screenshot", "mouse", "keyboard"},
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"kind":{"type":"string","enum":["move","click","type","key","scroll","screenshot","wait"]},` +
			`"x":{"type":"integer","minimum":0,"maximum":32767},` +
			`"y":{"type":"integer","minimum":0,"maximum":32767},` +
			`"dx":{"type":"integer","minimum":-10000,"maximum":10000},` +
			`"dy":{"type":"integer","minimum":-10000,"maximum":10000},` +
			`"text":{"type":"string","minLength":1,"maxLength":4096},` +
			`"key":{"type":"string","minLength":1,"maxLength":64},` +
			`"button":{"type":"string","enum":["left","right","middle"]},` +
			`"wait_ms":{"type":"integer","minimum":1,"maximum":30000}` +
			`},"required":["kind"],"additionalProperties":false}`),
	}}
}
