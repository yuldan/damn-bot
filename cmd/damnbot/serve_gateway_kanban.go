//go:build !minimal

package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/kanban"
)

// gatewayKanban is the /kanban adapter (SLASH-080). Mutations are owner-only:
// the adapter checks the role before the board operation, so a denial is
// never confused with an unknown subcommand.
type gatewayKanban struct{ store *kanban.Store }

func newGatewayKanban(db *ctxdb.Store) gateway.Kanban {
	workspace, err := os.Getwd()
	if err != nil {
		workspace = ""
	}
	return &gatewayKanban{store: kanban.New(db, workspace)}
}

func (g *gatewayKanban) Kanban(ctx context.Context, role gateway.Role, sessionID string, args []string) string {
	if len(args) == 0 {
		return "usage: /kanban <sub> … (list, show, create, assign, claim, comment, complete, block, unblock, stats, context)"
	}
	sub := args[0]
	if kanban.Mutating(sub) && role != gateway.RoleOwner {
		return "command /kanban " + cleanArg(sub) + " is owner-only"
	}
	inv := &kanban.Invocation{Store: g.store, Board: "default", Actor: "gateway:" + sessionID}
	v, err := inv.Run(ctx, sub, args[1:])
	if err != nil {
		if errors.Is(err, kanban.ErrInvalid) {
			return "kanban " + cleanArg(sub) + ": " + cleanLine(err.Error())
		}
		return "kanban " + cleanArg(sub) + ": " + cleanLine(err.Error())
	}
	return strings.TrimSpace(kanbanSummary(v))
}
