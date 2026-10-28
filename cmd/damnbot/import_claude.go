// `damnbot import claude-code` (SPEC §9.15 parity OPS-035; ADR-218). The
// mapping mirrors hermes_cli/agent_import.py; categories without a Damn-Bot
// seam are reported skipped, never silently dropped.
package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/importer"
	"github.com/damnbot/damnbot/internal/redact"
)

func runClaudeImport(o importOptions, stdout, stderr io.Writer) int {
	skip, ok := parseClaudeOnly(o.only, stderr)
	if !ok {
		return 2
	}
	root, err := resolveImportRoot(o.from, ".claude")
	if err != nil {
		return importFail(stderr, "claude-code", err)
	}
	ctx := context.Background()
	sink := &hermesSink{noWrite: noWrite{"import: writer is not available"}, root: root, red: redact.New(redact.Options{})}
	if !o.dryRun {
		_, dataDir, perr := skillsPaths()
		if perr != nil {
			return importFail(stderr, "claude-code", perr)
		}
		sink.dataDir = dataDir
	}
	imp, err := importer.NewClaude(&importer.ClaudeDeps{
		ReadFile: root.ReadFile, WalkDir: filepath.WalkDir, Sink: sink,
	})
	if err != nil {
		return importFail(stderr, "claude-code", err)
	}
	report, err := imp.Import(ctx, root.path, importer.ClaudeOptions{DryRun: o.dryRun, Skip: skip})
	var closeErr error
	if sink.db != nil {
		closeErr = sink.db.CloseContext(ctx)
	}
	if err != nil {
		return importFail(stderr, "claude-code", err)
	}
	if closeErr != nil {
		return importFail(stderr, "claude-code", closeErr)
	}
	return writeClaudeReport(stdout, root.path, &report, o.dryRun)
}

// parseClaudeOnly resolves --only for claude-code. Only memory and skills have
// a write seam; selecting settings/mcp/commands exits 2 instead of pretending.
func parseClaudeOnly(value string, stderr io.Writer) (map[string]string, bool) {
	skip := map[string]string{}
	if value == "" {
		return skip, true
	}
	wanted := map[string]bool{}
	for name := range strings.SplitSeq(value, ",") {
		name = strings.TrimSpace(name)
		switch name {
		case "memory", "skills":
			wanted[name] = true
		case "settings", "mcp", "commands":
			say(stderr, "import claude-code: --only %s is not supported in F3: no writer/store for that category (SPEC §9.15 parity; ADR-218); nothing was written\n", name)
			return nil, false
		default:
			say(stderr, "import claude-code: unknown --only category %s (settings,mcp,memory,skills,commands)\n", cleanArg(name))
			return nil, false
		}
	}
	for _, name := range []string{"memory", "skills"} {
		if !wanted[name] {
			skip[name] = "not selected (--only); nothing written"
		}
	}
	return skip, true
}

func writeClaudeReport(w io.Writer, root string, rep *importer.Report, dryRun bool) int {
	return writeReport(w, "claude-code", root, []reportCategory{
		{"settings", &rep.Config},
		{"mcp", &rep.MCP},
		{"memory", &rep.Memory},
		{"skills", &rep.Skills},
		{"commands", &rep.Cron},
	}, rep.ToS, nil, rep, dryRun)
}
