package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/importer"
	"github.com/damnbot/damnbot/internal/redact"
)

func runOpenClawImport(o importOptions, stdout, stderr io.Writer) int {
	skip, ok := parseAgentOnly(o.only, "openclaw", []string{"memory", "skills"}, []string{"cron"},
		"no cron store", stderr)
	if !ok {
		return 2
	}
	root, err := resolveOpenClawRoot(o.from)
	if err != nil {
		return importFail(stderr, "openclaw", err)
	}
	red := redact.New(redact.Options{})
	sink := &hermesSink{noWrite: noWrite{"import: writer is not available"}, root: root, red: red}
	if !o.dryRun {
		_, dataDir, perr := skillsPaths()
		if perr != nil {
			return importFail(stderr, "openclaw", perr)
		}
		sink.dataDir = dataDir
	}
	imp, err := importer.NewOpenClaw(&importer.OpenClawDeps{
		ReadFile: root.ReadFile, WalkDir: filepath.WalkDir, Redact: red.String, Sink: sink,
	})
	if err != nil {
		return importFail(stderr, "openclaw", err)
	}
	report, err := imp.Import(context.Background(), root.path, importer.OpenClawOptions{DryRun: o.dryRun, Skip: skip})
	var closeErr error
	if sink.db != nil {
		closeErr = sink.db.CloseContext(context.Background())
	}
	if err != nil {
		return importFail(stderr, "openclaw", err)
	}
	if closeErr != nil {
		return importFail(stderr, "openclaw", closeErr)
	}
	return writeOpenClawReport(stdout, root.path, &report, o.dryRun)
}

func resolveOpenClawRoot(from string) (hermesRoot, error) {
	if from != "" {
		return resolveImportRoot(from, "")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return hermesRoot{}, fmt.Errorf("home directory: %w", err)
	}
	var found []string
	for _, name := range []string{".openclaw", ".clawdbot", ".moltbot"} {
		path := filepath.Join(home, name)
		if info, serr := os.Stat(path); serr == nil && info.IsDir() {
			found = append(found, path)
		}
	}
	switch len(found) {
	case 0:
		return hermesRoot{}, fmt.Errorf("no OpenClaw home found (looked at %s); pass --from", ".openclaw, .clawdbot, .moltbot")
	case 1:
		return resolveImportRoot(found[0], "")
	default:
		return hermesRoot{}, fmt.Errorf("multiple OpenClaw homes found (%s); choose one with --from", strings.Join(found, ", "))
	}
}

func writeOpenClawReport(w io.Writer, root string, rep *importer.Report, dryRun bool) int {
	return writeReport(w, "openclaw", root, []reportCategory{
		{"memory", &rep.Memory},
		{"skills", &rep.Skills},
		{"cron", &rep.Cron},
		{"other", &rep.Config},
	}, rep.ToS, nil, rep, dryRun)
}
