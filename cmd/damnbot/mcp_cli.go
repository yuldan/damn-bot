package main

import (
	"io"
)

// runMCP is the `damnbot mcp` surface (SPEC §9.6, §10; F5-11). The command
// set is closed: an unknown subcommand is a usage error, never a stub.
func runMCP(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		mcpUsage(stderr)
		return 2
	}
	return runMCPImpl(args, stdout, stderr)
}

// mcpUsage is `damnbot help mcp` and the no-argument usage.
func mcpUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot mcp add|list|remove|refresh|review|auth [flags]\n\n")
	sessionsPrintf(w, "  add <name>       add [modules.mcp.servers.<name>] to config.toml\n")
	sessionsPrintf(w, "  list             configured servers and credential state\n")
	sessionsPrintf(w, "  remove <name>    revoke the token, then delete the config section\n")
	sessionsPrintf(w, "  refresh [name]   force tools/list through the pin gate\n")
	sessionsPrintf(w, "  review [name]    approve changed tool definitions (ADR-272)\n")
	sessionsPrintf(w, "  auth login|logout|status [name]\n")
	sessionsPrintf(w, "\ncommon flags: --json, --config <path>, --data-dir <dir>, --offline\n")
}

// mcpFail prints one sanitized error and returns the command exit code.
func mcpFail(w io.Writer, cmd string, err error) int {
	sessionsPrintf(w, "mcp %s: %s\n", cmd, cleanLine(err.Error()))
	return 1
}

func mcpNameArg(args []string, usage string, stderr io.Writer) (string, int) {
	if len(args) != 1 {
		sessionsPrintf(stderr, "mcp %s\n", usage)
		return "", 2
	}
	if !validMCPName(args[0]) {
		sessionsPrintf(stderr, "mcp: server name must match ^[a-z0-9][a-z0-9_-]{0,47}$\n")
		return "", 2
	}
	return args[0], 0
}

// validMCPName mirrors the config grammar; a name is never interpolated into
// a TOML key or a provider id unless it passes this check.
func validMCPName(name string) bool {
	if name == "" || len(name) > 48 {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

func mcpUsageErr(stderr io.Writer, format string, args ...any) int {
	sessionsPrintf(stderr, "mcp: "+format+"\n", args...)
	return 2
}
