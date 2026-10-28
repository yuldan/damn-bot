package main

import (
	"io"

	"github.com/damnbot/damnbot/internal/modules"
)

// configModules is `damnbot config modules [restart <id>]` (SPEC §10). The
// registry is the only source of truth: it is listed as compiled in, even
// when empty, and restart stays unavailable until a module engine exists
// (ADR-228). Nothing is started, frozen, or synthesized here.
func configModules(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return writeModuleList(stdout, modules.Default().Descriptors())
	}
	if args[0] != "restart" {
		say(stderr, "config modules: unknown subcommand %q\n", cleanArg(args[0]))
		configUsage(stderr)
		return 2
	}
	if len(args) != 2 {
		say(stderr, "config modules restart: one module id expected\n")
		return 2
	}
	say(stderr, "config modules restart: module %q was not restarted; unavailable in F3 (needs the serve/module engine, arrives in F4/F5)\n", cleanArg(args[1]))
	return 2
}

// writeModuleList prints one deterministic line per descriptor followed by the
// F3 status note. An empty registry prints zero lines, never a placeholder.
func writeModuleList(w io.Writer, descs []modules.Descriptor) int {
	if code := say(w, "modules: %d compiled in\n", len(descs)); code != 0 {
		return code
	}
	for _, d := range descs {
		if code := say(w, "  id=%s group=%s config=%s tools=%d\n",
			cleanArg(d.ID), cleanArg(d.Group), cleanArg(d.ConfigKeyOrID()), len(d.Tools)); code != 0 {
			return code
		}
	}
	return say(w, "note: the module engine is not wired in this build (F4/F5); [modules.<id>] is rejected as \"module section unavailable\" until then\n")
}

// configHooks is `damnbot config hooks ls|test <event>` (SPEC §10, §4.10).
// internal/hooks and the damnbot.hook/v1 dispatcher do not exist yet, so both
// shapes refuse with exit 2 instead of faking a listing or a test result.
func configHooks(args []string, _ io.Writer, stderr io.Writer) int {
	switch {
	case len(args) == 1 && args[0] == "ls":
		say(stderr, "config hooks ls: no hooks were listed; unavailable in F3: the hook engine (damnbot.hook/v1, SPEC §4.10) arrives in F4\n")
		return 2
	case len(args) == 2 && args[0] == "test":
		say(stderr, "config hooks test: event %q was not tested; unavailable in F3: the hook engine (damnbot.hook/v1, SPEC §4.10) arrives in F4\n", cleanArg(args[1]))
		return 2
	default:
		say(stderr, "config hooks: usage: damnbot config hooks ls|test <event>\n")
		return 2
	}
}
