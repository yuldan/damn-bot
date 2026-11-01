//go:build !minimal

package gateway

import (
	"context"
	"fmt"
)

// reloadMCP re-reads [modules.mcp] and drops live clients, so a config edit
// takes effect without restarting the daemon. Only the owner may call it.
func (c *Commands) mcpReloadResult(ctx context.Context) CommandResult {
	if c.mcp == nil {
		return CommandResult{Handled: true, Reply: "command /reload-mcp is not supported in this build"}
	}
	n, err := c.mcp.ReloadMCP(ctx)
	if err != nil {
		return CommandResult{Handled: true, Reply: "mcp reload failed; previous servers stay active"}
	}
	return CommandResult{Handled: true, Reply: fmt.Sprintf("mcp: %d server(s) reloaded", n)}
}
