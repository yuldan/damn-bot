//go:build !minimal

package browser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// taint labels every browser result with the navigated host of the current
// page (SPEC §14.5, ADR-070).
func (m *Module) taint() string {
	host := m.host
	if host == "" {
		host = "page"
	}
	return "browser:" + host
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func decodeArgs(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %w", errBadConfig, err)
	}
	return nil
}

func invalidArgs(err error) error {
	return &host.Error{Code: "invalid_args", Message: err.Error()}
}
