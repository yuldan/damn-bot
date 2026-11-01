//go:build !minimal

package main

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/codeintel"
	"github.com/damnbot/damnbot/internal/tools"
)

// codeIntelMapper renders repo maps for the read-only tools.
type codeIntelMapper struct{ cwd string }

func (m codeIntelMapper) RepoMap(ctx context.Context, root, focus string, maxTokens int) (string, error) {
	idx, err := codeintel.Index(ctx, codeintel.Options{
		Root: root, Focus: focus, Languages: []string{"c", "cpp"},
	})
	if err != nil {
		return "", fmt.Errorf("codeintel: %w", err)
	}
	return idx.Render(maxTokens), nil
}

// codeIntelToolsLinked reports whether repo_map/code_intel are wired (full build).
const codeIntelToolsLinked = true

// withCodeIntelTools appends repo_map/code_intel (!minimal: the tree-sitter
// asset is only linked in the full binary).
func withCodeIntelTools(roots []string, cwd string, trusted bool, list []tools.Tool) []tools.Tool {
	return append(list, tools.NewCodeIntelTools(&tools.CodeIntelConfig{
		Mapper: codeIntelMapper{cwd: cwd}, Roots: roots, Cwd: cwd, Trusted: trusted,
	})...)
}
