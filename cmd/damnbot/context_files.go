package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

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

// contextTierInput builds the context tier of one turn (MEM-026, SPEC §4.8):
// the workspace instruction files read through the sandbox and wrapped as data
// when the workspace is untrusted. degraded selects the PROV-02 profile. It
// lives here, not in run.go, to keep the composition root inside the file
// length gate (E-14).
func contextTierInput(ctx context.Context, model *runModel, o *runOptions, degraded bool) *assemble.ContextInput {
	in := &assemble.ContextInput{
		Facts:          nudgeFacts(ctx, model) + "os=" + runtime.GOOS + "; shell=headless; cwd=" + model.cwd + "; frontend=headless",
		Degraded:       degraded,
		Files:          contextFiles(),
		Reader:         model.reader,
		UntrustedFiles: !model.trusted,
	}
	if o.retrieverFor != nil {
		in.Query = o.prompt
		in.Retriever = o.retrieverFor(model.db)
	}
	return in
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
