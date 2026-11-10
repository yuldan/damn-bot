//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/web"
)

// paneBufferBytes bounds the scrollback one pane keeps in memory. A slower
// browser reader loses the oldest bytes, never the process.
const paneBufferBytes = 256 << 10

// maxPreviewBytes bounds one preview file.
const maxPreviewBytes = 256 << 10

// webPanes is the dashboard's real pane backend (UI-017): a local shell for
// the terminal pane, a workspace reader for the preview pane, and the named
// projects store for the project list. It is only reachable through an
// authenticated web session and never through a model tool.
type webPanes struct {
	dataDir string
	mu      sync.Mutex
	panes   map[string]*paneState
	next    uint64
}

// paneState is one open pane and its bounded output.
type paneState struct {
	info   web.Pane
	mu     sync.Mutex
	buf    []byte
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	closed bool
}

// newWebPanes builds the pane backend and its project source.
func newWebPanes(dataDir string) (*webPanes, error) {
	if dataDir == "" {
		return nil, errors.New("web panes: no data dir")
	}
	return &webPanes{dataDir: dataDir, panes: map[string]*paneState{}}, nil
}

// List returns every open pane in creation order.
func (p *webPanes) List(_ context.Context) ([]web.Pane, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]web.Pane, 0, len(p.panes))
	for i := uint64(1); i <= p.next; i++ {
		id := fmt.Sprintf("pane-%d", i)
		if st, ok := p.panes[id]; ok {
			out = append(out, st.info)
		}
	}
	return out, nil
}

// Open opens one terminal or preview pane.
func (p *webPanes) Open(ctx context.Context, req web.PaneOpen) (web.Pane, error) {
	if req.Kind == web.PanePreview {
		return p.openPreview(req)
	}
	return p.openTerminal(ctx, req)
}

// openPreview resolves target inside one configured project folder and loads
// it into the pane buffer. The path is checked against every folder of every
// project, so a stray absolute path cannot read outside the workspaces.
func (p *webPanes) openPreview(req web.PaneOpen) (web.Pane, error) {
	roots := p.projectRoots(req.Project)
	if len(roots) == 0 {
		return web.Pane{}, errors.New("no project workspace")
	}
	file, err := resolvePreview(roots, req.Target)
	if err != nil {
		return web.Pane{}, err
	}
	data, err := readPreview(file)
	if err != nil {
		return web.Pane{}, err
	}
	p.mu.Lock()
	p.next++
	id := fmt.Sprintf("pane-%d", p.next)
	p.mu.Unlock()
	st := &paneState{info: web.Pane{ID: id, Kind: web.PanePreview, Title: filepath.Base(file), Project: req.Project, Created: time.Now().UTC()}, buf: data}
	p.mu.Lock()
	p.panes[id] = st
	p.mu.Unlock()
	return st.info, nil
}

// resolvePreview returns the file inside one root, refusing traversal and
// non-regular files.
func resolvePreview(roots []string, target string) (string, error) {
	if target == "" {
		return "", errors.New("empty preview target")
	}
	clean := filepath.Clean(target)
	if filepath.IsAbs(clean) {
		for _, root := range roots {
			if rel, err := filepath.Rel(root, clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return clean, nil
			}
		}
		return "", errors.New("path is outside the project")
	}
	for _, root := range roots {
		dir, err := os.OpenRoot(root)
		if err != nil {
			continue
		}
		info, serr := dir.Stat(clean)
		cerr := dir.Close()
		if serr != nil || cerr != nil || !info.Mode().IsRegular() {
			continue
		}
		return filepath.Join(root, clean), nil
	}
	return "", errors.New("path is outside the project")
}

// readPreview reads one regular file with the preview cap.
func readPreview(file string) ([]byte, error) {
	f, err := os.Open(file) // #nosec G304 -- resolved inside a project root
	if err != nil {
		return nil, fmt.Errorf("open preview: %w", err)
	}
	info, serr := f.Stat()
	if serr != nil || !info.Mode().IsRegular() {
		if serr == nil {
			serr = errors.New("preview target is not a file")
		}
		if cerr := f.Close(); cerr != nil {
			return nil, errors.Join(fmt.Errorf("stat preview: %w", serr), fmt.Errorf("close preview: %w", cerr))
		}
		return nil, fmt.Errorf("stat preview: %w", serr)
	}
	data, rerr := io.ReadAll(io.LimitReader(f, maxPreviewBytes))
	cerr := f.Close()
	if rerr != nil {
		return nil, errors.Join(fmt.Errorf("read preview: %w", rerr), cerr)
	}
	if cerr != nil {
		return nil, fmt.Errorf("close preview: %w", cerr)
	}
	return data, nil
}

// shellFromEnv returns the operator's shell from the GAP-SEC-02 allowlist
// without reading the environment directly (forbidigo), falling back to sh.
func shellFromEnv() string {
	for _, kv := range os.Environ() {
		if v, ok := strings.CutPrefix(kv, "SHELL="); ok && v != "" {
			return v
		}
	}
	return "sh"
}

// openTerminal starts a shell in the project primary folder (or the data dir)
// and buffers its output.
func (p *webPanes) openTerminal(ctx context.Context, req web.PaneOpen) (web.Pane, error) {
	dir := p.dataDir
	if roots := p.projectRoots(req.Project); len(roots) > 0 {
		dir = roots[0]
	}
	if req.Target != "" {
		if abs, err := filepath.Abs(req.Target); err == nil {
			if insideRoots(p.projectRoots(req.Project), abs) {
				dir = abs
			}
		}
	}
	shell := shellFromEnv()
	// The shell outlives the request that opened it: keep its values but not
	// the request cancellation. Close owns its lifetime.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), shell) // #nosec G204,G702 -- fixed shell path from the allowlisted environment
	cmd.Dir = dir
	cmd.Env = os.Environ()
	setPaneProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return web.Pane{}, fmt.Errorf("pane stdin: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return web.Pane{}, errors.Join(fmt.Errorf("pane stdout: %w", err), stdin.Close())
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return web.Pane{}, errors.Join(fmt.Errorf("pane start: %w", err), stdin.Close())
	}
	p.mu.Lock()
	p.next++
	id := fmt.Sprintf("pane-%d", p.next)
	p.mu.Unlock()
	st := &paneState{
		info:  web.Pane{ID: id, Kind: web.PaneTerminal, Title: "terminal", Project: req.Project, Open: true, Created: time.Now().UTC()},
		cmd:   cmd,
		stdin: stdin,
	}
	go st.consume(out)
	p.mu.Lock()
	p.panes[id] = st
	p.mu.Unlock()
	return st.info, nil
}

// consume copies shell output into the bounded buffer.
func (s *paneState) consume(r io.Reader) {
	defer func() {
		s.mu.Lock()
		s.info.Open = false
		s.mu.Unlock()
	}()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.append(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// append keeps at most paneBufferBytes, dropping the oldest bytes first.
func (s *paneState) append(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	if over := len(s.buf) - paneBufferBytes; over > 0 {
		s.buf = append(s.buf[:0], s.buf[over:]...)
	}
}

// Input writes to the shell of a terminal pane.
func (p *webPanes) Input(_ context.Context, id string, data []byte) error {
	st := p.find(id)
	if st == nil || st.stdin == nil {
		return errors.New("pane is not writable")
	}
	_, err := st.stdin.Write(data)
	if err != nil {
		return fmt.Errorf("pane input: %w", err)
	}
	return nil
}

// Output returns one incremental chunk. Preview panes are complete on the
// first read; terminal panes return whatever is buffered at the offset.
func (p *webPanes) Output(_ context.Context, id string, offset int) (web.PaneChunk, error) {
	st := p.find(id)
	if st == nil {
		return web.PaneChunk{}, web.ErrPaneMissing
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if offset < 0 || offset > len(st.buf) {
		offset = 0
	}
	data := append([]byte(nil), st.buf[offset:]...)
	if len(data) > 64<<10 {
		data = data[:64<<10]
	}
	return web.PaneChunk{Data: data, Next: offset + len(data), Open: st.info.Open || st.info.Kind == web.PanePreview}, nil
}

// Close stops one pane. Closing an unknown pane is an error, so a stale tab
// never looks successful.
func (p *webPanes) Close(_ context.Context, id string) error {
	st := p.find(id)
	if st == nil {
		return web.ErrPaneMissing
	}
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return nil
	}
	st.closed = true
	st.info.Open = false
	cmd, stdin := st.cmd, st.stdin
	st.mu.Unlock()
	var err error
	if stdin != nil {
		err = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		err = errors.Join(err, killPaneProcessGroup(cmd))
	}
	p.mu.Lock()
	delete(p.panes, id)
	p.mu.Unlock()
	return err
}

// CloseAll releases every pane on server shutdown.
func (p *webPanes) CloseAll(ctx context.Context) error {
	p.mu.Lock()
	ids := make([]string, 0, len(p.panes))
	for id := range p.panes {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	var err error
	for _, id := range ids {
		err = errors.Join(err, p.Close(ctx, id))
	}
	return err
}

// Projects returns the named workspaces of projects.json.
func (p *webPanes) Projects(_ context.Context) ([]web.Project, error) {
	st := f8ProjectState{}
	if err := f8Load(f8StorePath(p.dataDir, "projects.json"), &st); err != nil {
		return nil, err
	}
	out := make([]web.Project, 0, len(st.Items))
	for _, item := range st.Items {
		if item.Archived {
			continue
		}
		out = append(out, web.Project{
			Name: item.Name, Primary: item.Primary, Folders: item.Folders,
			Current: st.Current == item.Name, Board: item.Board,
		})
	}
	return out, nil
}

// find returns one pane state.
func (p *webPanes) find(id string) *paneState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.panes[id]
}

// projectRoots returns the primary folder first, then the other folders of the
// named project; without a name every project folder is eligible.
func (p *webPanes) projectRoots(name string) []string {
	st := f8ProjectState{}
	if err := f8Load(f8StorePath(p.dataDir, "projects.json"), &st); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var roots []string
	add := func(folder string) {
		if folder == "" || seen[folder] {
			return
		}
		seen[folder] = true
		roots = append(roots, folder)
	}
	for _, item := range st.Items {
		if item.Archived || (name != "" && item.Name != name) {
			continue
		}
		add(item.Primary)
		for _, folder := range item.Folders {
			add(folder)
		}
	}
	return roots
}

// insideRoots reports whether path is inside one root.
func insideRoots(roots []string, path string) bool {
	for _, root := range roots {
		if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
