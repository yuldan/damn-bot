package main

import (
	"context"
	"io"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/importer"
	"github.com/damnbot/damnbot/internal/redact"
)

func runCodexImport(o importOptions, stdout, stderr io.Writer) int {
	skip, ok := parseAgentOnly(o.only, "codex", []string{"memory", "skills"}, []string{"mcp"},
		"no MCP store", stderr)
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

func writeCodexReport(w io.Writer, root string, rep *importer.Report, dryRun bool) int {
	return writeReport(w, "codex", root, []reportCategory{
		{"mcp", &rep.MCP},
		{"memory", &rep.Memory},
		{"skills", &rep.Skills},
	}, rep.ToS, nil, rep, dryRun)
}
