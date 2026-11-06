//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
)

func newGatewayKanban(_ *ctxdb.Store) gateway.Kanban { return nil }
