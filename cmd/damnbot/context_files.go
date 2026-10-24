package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/sandbox"
)

// contextFileNames is the workspace context tier of SPEC §4.8 / §12 table
// (MEM-026): the agent instruction files of the project, read from the
// session working directory in this priority order.
func contextFileNames() []string {
	return []string{"AGENTS.md", "CLAUDE.md", "SOUL.md", ".cursorrules", ".hermes.md"}
}

// contextFiles returns the ContextFile list for one run.
func contextFiles() []assemble.ContextFile {
	names := contextFileNames()
	out := make([]assemble.ContextFile, 0, len(names))
	for _, name := range names {
		out = append(out, assemble.ContextFile{Path: name})
	}
	return out
}

// sandboxReader reads context files through the sandbox: a path that leaves
// the workspace root, resolves to a deny_paths entry or points at something
// that is not a regular file is refused instead of read (SPEC §4.8, §14).
type sandboxReader struct {
	sb   *sandbox.Sandbox
	root string // session working directory, a sandbox root
}

// Open returns the file at path relative to the workspace root. A refused
// path is assemble.ErrUnavailable and a missing file is fs.ErrNotExist, so
// the caller omits that context file instead of failing the run.
func (r sandboxReader) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	f, verdict, err := r.sb.OpenFile(ctx, filepath.Join(r.root, path), os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("workspace context file %s: %w", path, err)
	}
	if verdict != sandbox.Inside || f == nil {
		return nil, assemble.ErrUnavailable
	}
	return f, nil
}
