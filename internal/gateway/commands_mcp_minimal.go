//go:build minimal

package gateway

import "context"

func (*Commands) mcpReloadResult(context.Context) CommandResult {
	return CommandResult{Handled: true, Reply: "command /reload-mcp is not supported in this build"}
}
