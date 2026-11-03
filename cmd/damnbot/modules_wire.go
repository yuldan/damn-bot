package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/tools"
)

// errNoModuleHost refuses to start modules without the composition-root
// host (SPEC §3.2.1): a Factory must never receive a nil Host.
var errNoModuleHost = errors.New("modules are compiled in but the module host is not wired yet")

// start is main without os.Exit: frozen is modules.Freeze(), evaluated after
// registerModules in main (E-10), and a registry error ends the process before
// any command runs (ADR-119: Validate at startup, never panic).
func start(frozen error, args []string, stdout, stderr io.Writer) int {
	if frozen != nil {
		if _, err := fmt.Fprintf(stderr, "damnbot: module registry: %v\n", frozen); err != nil {
			return 1
		}
		return 1
	}
	return run(args, stdout, stderr)
}

// runModules is the deferred-tool wiring of one run: the dispatcher config,
// the lifecycle runtime (Shutdown on close) and the meta-tools to register.
// The zero value means "no deferred tools".
type runModules struct {
	deferred *tools.Deferred
	runtime  *modules.Runtime
	meta     []tools.Tool
}

// moduleDeps is the composition-root input of newRunModules. Enabled and
// Config are the runtime predicates of [modules.Deps]; a nil Enabled means
// every registered module is on (tests), never a production default.
type moduleDeps struct {
	Registry *modules.Registry
	Host     host.Host
	Enabled  func(id string) bool
	Config   func(id string) host.Config
}

// newRunModules builds the deferred catalog of d.Registry (ADR-119). A
// registered but disabled module contributes no tool: the catalog, tool_search
// and tool_invoke stay out of the payload, so an unwired/disabled module can
// never be selected (P1 unchanged). With no enabled module tool nothing is
// wired and the dispatcher keeps the F1 unknown-tool behaviour. Modules
// without a host refuse to start instead of receiving a nil Host.
func newRunModules(d moduleDeps) (runModules, error) {
	reg := d.Registry
	if reg == nil {
		reg = modules.Default()
	}
	if err := reg.Freeze(); err != nil { // idempotent: re-validates the frozen set
		return runModules{}, fmt.Errorf("module registry: %w", err)
	}
	var list []tools.DeferredTool
	for _, desc := range reg.Descriptors() {
		if d.Enabled != nil && !d.Enabled(desc.ID) {
			continue
		}
		for _, t := range desc.Tools {
			list = append(list, tools.DeferredTool{
				Name: t.Name, Description: t.Description, Group: desc.Group, Module: desc.ID,
				Effect: t.Effect, Tags: t.Tags, Schema: t.Schema,
			})
		}
	}
	if len(list) == 0 {
		return runModules{}, nil
	}
	if d.Host == nil {
		return runModules{}, fmt.Errorf("%d deferred tools: %w", len(list), errNoModuleHost)
	}
	cat, err := tools.NewCatalog(list)
	if err != nil {
		return runModules{}, fmt.Errorf("deferred catalog: %w", err)
	}
	rt := modules.NewRuntime(modules.Deps{Registry: reg, Host: d.Host, Enabled: d.Enabled, Config: d.Config})
	status := func(id string) string { return string(rt.Status(id)) }
	return runModules{
		deferred: &tools.Deferred{Catalog: cat, Runtime: rt},
		runtime:  rt,
		meta:     []tools.Tool{tools.NewToolSearch(cat, status), tools.NewToolInvoke()},
	}, nil
}
