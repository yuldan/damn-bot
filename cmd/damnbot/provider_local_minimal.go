//go:build minimal

package main

import "github.com/damnbot/damnbot/internal/provider/registry"

// localOpenAIBase is a no-op in the minimal build: local openai-chat routes
// keep the catalog URL byte-for-byte (the /v1 alias is an F5 full-build
// convenience; the minimal binary has no room for it).
func localOpenAIBase(_ *registry.Provider, base string, _ []registry.Protocol, _ bool) string {
	return base
}
