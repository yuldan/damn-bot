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

// start is main without os.Exit: frozen is modules.Freeze(), evaluated first
// in main (E-10), and a registry error ends the process before any command
// runs (ADR-119: Validate at startup, never panic).
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

// newRunModules builds the deferred catalog of reg (ADR-119). With no module
// tool nothing is wired: tool_search/tool_invoke are not advertised
// (TestPromptNoUnavailableTools) and the dispatcher keeps the F1 unknown-tool
// behaviour, so the F1 tool list and the P1 prompt are unchanged. Modules
// without a host refuse to start instead of receiving a nil Host.
func newRunModules(reg *modules.Registry, h host.Host) (runModules, error) {
	if err := reg.Freeze(); err != nil { // idempotent: re-validates the frozen set
		return runModules{}, fmt.Errorf("module registry: %w", err)
	}
	var list []tools.DeferredTool
	for _, d := range reg.Descriptors() {
		for _, t := range d.Tools {
			list = append(list, tools.DeferredTool{
				Name: t.Name, Description: t.Description, Group: d.Group, Module: d.ID,
				Effect: t.Effect, Tags: t.Tags, Schema: t.Schema,
			})
		}
	}
	if len(list) == 0 {
		return runModules{}, nil
	}
	if h == nil {
		return runModules{}, fmt.Errorf("%d deferred tools: %w", len(list), errNoModuleHost)
	}
	cat, err := tools.NewCatalog(list)
	if err != nil {
		return runModules{}, fmt.Errorf("deferred catalog: %w", err)
	}
	rt := modules.NewRuntime(modules.Deps{Registry: reg, Host: h})
	status := func(id string) string { return string(rt.Status(id)) }
	return runModules{
		deferred: &tools.Deferred{Catalog: cat, Runtime: rt},
		runtime:  rt,
		meta:     []tools.Tool{tools.NewToolSearch(cat, status), tools.NewToolInvoke()},
	}, nil
}
