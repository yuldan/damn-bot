//go:build !minimal

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/mcpstore"
	"github.com/damnbot/damnbot/internal/mcp"
)

// mcpRefresh forces tools/list through the pin gate (ADR-272).
func mcpRefresh(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return 2
	}
	if len(pos) > 1 {
		return mcpUsageErr(stderr, "refresh takes at most one server name")
	}
	env, err := openCLI(context.Background(), "", "")
	if err != nil {
		return mcpFail(stderr, "refresh", err)
	}
	defer mcpCloseDB(env.db, stderr)
	servers := namedMCPServers(&env.cfg, pos)
	if len(servers) == 0 {
		return mcpFail(stderr, "refresh", errors.New("no server selected"))
	}
	host := cliMCPHost(env.db, env.loader, stdin(), stdout, stderr)
	failed := 0
	for i := range servers {
		srv := &servers[i]
		client, cerr := newMCPClient(host, *srv, &env.cfg)
		if cerr != nil {
			sessionsPrintf(stderr, "mcp refresh %s: %s\n", srv.Name, cleanLine(cerr.Error()))
			failed++
			continue
		}
		tools, lerr := client.RefreshTools(context.Background())
		closeErr := client.Close()
		if lerr != nil {
			sessionsPrintf(stderr, "mcp refresh %s: %s\n", srv.Name, cleanLine(lerr.Error()))
			failed++
			continue
		}
		if closeErr != nil {
			sessionsPrintf(stderr, "mcp refresh %s: close: %s\n", srv.Name, cleanLine(closeErr.Error()))
		}
		sessionsPrintf(stdout, "%s: %d tools\n", srv.Name, len(tools))
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// mcpReview approves changed definitions; a quarantined one is not approvable.
func mcpReview(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tool := fs.String("tool", "", "approve one changed tool instead of every changed tool")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return 2
	}
	if len(pos) > 1 {
		return mcpUsageErr(stderr, "review takes at most one server name")
	}
	env, err := openCLI(context.Background(), "", "")
	if err != nil {
		return mcpFail(stderr, "review", err)
	}
	defer mcpCloseDB(env.db, stderr)
	servers := namedMCPServers(&env.cfg, pos)
	if len(servers) == 0 {
		return mcpFail(stderr, "review", errors.New("no server selected"))
	}
	host := cliMCPHost(env.db, env.loader, stdin(), stdout, stderr)
	failed := 0
	for i := range servers {
		if mcpReviewServer(host, &env.cfg, &servers[i], *tool, stdout, stderr) {
			failed++
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func mcpReviewServer(host *mcpCLIHost, cfg *config.Config, srv *mcp.Server, tool string, stdout, stderr io.Writer) bool {
	client, cerr := newMCPClient(host, *srv, cfg)
	if cerr != nil {
		sessionsPrintf(stderr, "mcp review %s: %s\n", srv.Name, cleanLine(cerr.Error()))
		return true
	}
	defer func() {
		if err := client.Close(); err != nil {
			sessionsPrintf(stderr, "mcp review %s: close: %s\n", srv.Name, cleanLine(err.Error()))
		}
	}()
	pins, perr := client.Pins(context.Background())
	if perr != nil {
		sessionsPrintf(stderr, "mcp review %s: %s\n", srv.Name, cleanLine(perr.Error()))
		return true
	}
	failed := false
	for i := range pins {
		pin := &pins[i]
		if tool != "" && pin.Tool != tool {
			continue
		}
		switch pin.State {
		case mcp.PinPinned:
			sessionsPrintf(stdout, "%s/%s: pinned\n", srv.Name, pin.Tool)
		case mcp.PinQuarantined:
			sessionsPrintf(stdout, "%s/%s: quarantined (removed definition; not approvable)\n", srv.Name, pin.Tool)
		case mcp.PinChanged:
			sessionsPrintf(stdout, "%s/%s: changed; reviewing\n", srv.Name, pin.Tool)
			if rerr := client.Review(context.Background(), pin.Tool, "cli"); rerr != nil {
				sessionsPrintf(stderr, "mcp review %s/%s: %s\n", srv.Name, pin.Tool, cleanLine(rerr.Error()))
				failed = true
			}
		}
	}
	return failed
}

func mcpCloseDB(db *ctxdb.Store, stderr io.Writer) {
	if db == nil {
		return
	}
	if err := db.Close(); err != nil {
		sessionsPrintf(stderr, "mcp: close db: %s\n", cleanLine(err.Error()))
	}
}

func pinStoreFor(db *ctxdb.Store) (mcp.PinStore, error) {
	if db == nil {
		return nil, errors.New("mcp: no database for pins")
	}
	return cliPins{store: mcpstore.Store{DB: db}}, nil
}

type cliPins struct{ store mcpstore.Store }

func (p cliPins) Pin(ctx context.Context, server, tool string) (mcp.Pin, bool, error) {
	pin, ok, err := p.store.Pin(ctx, server, tool)
	if err != nil {
		return mcp.Pin{}, false, fmt.Errorf("mcp: read pin: %w", err)
	}
	return toMCPPin(&pin), ok, nil
}

func (p cliPins) Pins(ctx context.Context, server string) ([]mcp.Pin, error) {
	pins, err := p.store.Pins(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("mcp: list pins: %w", err)
	}
	out := make([]mcp.Pin, len(pins))
	for i := range pins {
		out[i] = toMCPPin(&pins[i])
	}
	return out, nil
}

func (p cliPins) PutPin(ctx context.Context, pin mcp.Pin) error { //nolint:gocritic // PinStore fixes the value signature
	err := p.store.PutPin(ctx, mcpstore.Pin{
		Server: pin.Server, Tool: pin.Tool, SchemaSHA256: pin.SchemaSHA256,
		FirstSeen: pin.FirstSeen, ApprovedAt: pin.ApprovedAt, ApprovedBy: pin.ApprovedBy,
		State: mcpstore.State(pin.State), Diff: pin.Diff,
	})
	if err != nil {
		return fmt.Errorf("mcp: put pin: %w", err)
	}
	return nil
}

func (p cliPins) ApprovePin(ctx context.Context, server, tool, digest, by string, at int64) (bool, error) {
	ok, err := p.store.ApprovePin(ctx, server, tool, digest, by, at)
	if err != nil {
		return false, fmt.Errorf("mcp: approve pin: %w", err)
	}
	return ok, nil
}

func toMCPPin(pin *mcpstore.Pin) mcp.Pin {
	return mcp.Pin{
		Server: pin.Server, Tool: pin.Tool, SchemaSHA256: pin.SchemaSHA256,
		FirstSeen: pin.FirstSeen, ApprovedAt: pin.ApprovedAt, ApprovedBy: pin.ApprovedBy,
		State: mcp.PinState(pin.State), Diff: pin.Diff,
	}
}
