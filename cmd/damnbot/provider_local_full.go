//go:build !minimal

package main

import (
	"net/url"
	"strings"

	"github.com/damnbot/damnbot/internal/provider/registry"
)

// localOpenAIBase appends /v1 to a local catalog alias that serves the
// OpenAI-compatible API from a bare host (SPEC §4.1: "/v1 ditambahkan
// otomatis untuk alias lokal"). An explicit path is preserved, and a base
// fromCatalog=false is an operator override, never rewritten.
func localOpenAIBase(p *registry.Provider, base string, protocols []registry.Protocol, fromCatalog bool) string {
	if !fromCatalog || !p.Local || !hasProtocol(protocols, registry.ProtocolOpenAIChat) {
		return base
	}
	u, err := url.Parse(base)
	if err != nil || (u.Path != "" && u.Path != "/") {
		return base
	}
	return strings.TrimRight(base, "/") + "/v1"
}
