//go:build !minimal

package main

import (
	"context"
	"flag"
	"io"
)

// runImportArchive implements `damnbot import archive <file>` (SPEC §10,
// GAP-OPS-05). The archive is a DMBK1 container; every skill and memory item
// goes through the same threat scan and quarantine as any other import.
func runImportArchive(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import archive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	conflict := fs.String("conflict", string(keepLocal), "conflict resolution: keep-local, take-remote, or both")
	dry := fs.Bool("dry-run", false, "report what would change; write nothing")
	dataDir := fs.String("data-dir", "", "override the data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 || pos[0] == "" {
		importUsage(stderr)
		return 2
	}
	mode, err := parseConflictMode(*conflict)
	if err != nil {
		say(stderr, "import archive: %s\n", cleanLine(err.Error()))
		return 2
	}
	p, err := sessionPaths(*dataDir)
	if err != nil {
		return importFail(stderr, "archive", err)
	}
	st, err := secretStore(p)
	if err != nil {
		return importFail(stderr, "archive", err)
	}
	ctx := context.Background()
	h, err := readMeta(pos[0])
	if err != nil {
		return importFail(stderr, "archive", err)
	}
	key, err := st.BackupByID(ctx, h.RecipientID)
	if err != nil {
		return importFail(stderr, "archive", err)
	}
	defer key.Zero()
	codec, err := dataCodec(ctx, p, st)
	if err != nil {
		return importFail(stderr, "archive", err)
	}
	counts, err := importPortable(ctx, p, pos[0], mode, *dry, key, codec)
	if err != nil {
		return importFail(stderr, "archive", err)
	}
	if _, werr := io.WriteString(stdout, "archive: "+portableReportLine(counts, *dry)+"\n"); werr != nil {
		return 1
	}
	return 0
}
