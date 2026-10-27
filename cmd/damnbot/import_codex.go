// `damnbot import codex` (SPEC §9.15 parity OPS-035; ADR-218). Mapping mirrors
// hermes_cli/agent_import.py; mcp_servers has no store yet and is reported skipped.
package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/importer"
	"github.com/damnbot/damnbot/internal/redact"
)

func runCodexImport(o importOptions, stdout, stderr io.Writer) int {
	skip, ok := parseCodexOnly(o.only, stderr)
	if !ok {
		return 2
	}
	root, err := resolveImportRoot(o.from, ".codex")
	if err != nil {
		return importFail(stderr, "codex", err)
	}
	ctx := context.Background()
	red := redact.New(redact.Options{})
	sink := &hermesSink{noWrite: noWrite{"import: writer is not available"}, root: root, red: red}
	if !o.dryRun {
		_, dataDir, perr := skillsPaths()
		if perr != nil {
			return importFail(stderr, "codex", perr)
		}
		sink.dataDir = dataDir
	}
	imp, err := importer.NewCodex(&importer.CodexDeps{
		ReadFile: root.ReadFile, WalkDir: filepath.WalkDir, Redact: red.String, Sink: sink,
	})
	if err != nil {
		return importFail(stderr, "codex", err)
	}
	report, err := imp.Import(ctx, root.path, importer.CodexOptions{DryRun: o.dryRun, Skip: skip})
	var closeErr error
	if sink.db != nil {
		closeErr = sink.db.CloseContext(ctx)
	}
	if err != nil {
		return importFail(stderr, "codex", err)
	}
	if closeErr != nil {
		return importFail(stderr, "codex", closeErr)
	}
	return writeCodexReport(stdout, root.path, &report, o.dryRun)
}

// parseCodexOnly resolves --only for codex: only memory and skills have a
// write seam; --only mcp exits 2 instead of pretending.
func parseCodexOnly(value string, stderr io.Writer) (map[string]string, bool) {
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
		case "mcp":
			say(stderr, "import codex: --only mcp is not supported in F3: no MCP store (SPEC §9.15 parity; ADR-218); nothing was written\n")
			return nil, false
		default:
			say(stderr, "import codex: unknown --only category %s (mcp,memory,skills)\n", cleanArg(name))
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

func writeCodexReport(w io.Writer, root string, rep *importer.Report, dryRun bool) int {
	return writeReport(w, "codex", root, []reportCategory{
		{"mcp", &rep.MCP},
		{"memory", &rep.Memory},
		{"skills", &rep.Skills},
	}, nil, rep, dryRun)
}
