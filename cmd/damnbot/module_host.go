//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// runModuleHost is the composition root's [host.Host] for one `run` session.
// It owns the capability seam (approvals, allowlisted child environment and
// clock), the ledger-backed KV namespace, Spawn, and the event sink. LLM is
// nil until a module borrows the ledger; Budget keeps the fail-closed default
// (CapabilitiesOf), so a module that tries to spend is denied.
//
// Emit writes module_start/module_error into the session transcript; the
// runtime already swallows a lost notice, so a store failure is reported and
// returned rather than retried.
type runModuleHost struct {
	*mcpCLIHost
	db  *ctxdb.Store
	sid string
}

// newRunModuleHost builds the host for one session. A nil store has no host:
// modules refuse to start rather than receive a half-wired capability seam
// (errNoModuleHost).
func newRunModuleHost(db *ctxdb.Store, sid string) host.Host {
	if db == nil || sid == "" {
		return nil
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return nil
	}
	return &runModuleHost{
		mcpCLIHost: &mcpCLIHost{
			db:      db,
			approve: &terminalApprovals{in: os.Stdin, out: os.Stderr},
			secrets: envSecrets{getenv: loader.Env},
			clock:   mcpWallClock{},
			errOut:  os.Stderr,
		},
		db:  db,
		sid: sid,
	}
}

// Emit appends one module event to the session transcript. The module id is
// the event content and the typed payload stays JSON, so `sessions view`
// renders it without a second schema.
func (h *runModuleHost) Emit(ctx context.Context, ev host.Event) error {
	payload := string(ev.Payload)
	if payload == "" {
		payload = "{}"
	} else if !json.Valid(ev.Payload) {
		return errors.New("module event payload is not JSON")
	}
	if _, err := h.db.Append(ctx, h.sid, nil, ctxdb.Event{
		Kind: string(ev.Kind), Content: ev.Content, Payload: payload,
	}); err != nil {
		if h.errOut != nil {
			sessionsPrintf(h.errOut, "module %s: %s\n", ev.Content, cleanLine(err.Error()))
		}
		return fmt.Errorf("append module event: %w", err)
	}
	return nil
}
