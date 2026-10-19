package codetools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/sandbox"
)

// walkSpec configures one ordered parallel walk (CODE-TOOLS §2.2, §3).
type walkSpec struct {
	root   string // resolved absolute directory (sandbox-checked)
	hidden bool   // include dot entries (explicit path or dot glob)
	// approved: root is an approved outside_root path (SPEC §4.3 langkah 7).
	approved bool
	// keepDir prunes a directory by its path relative to root ("a/b").
	keepDir func(rel string) bool
	// keepFile filters a file by its path relative to root.
	keepFile func(rel string) bool
	// dirs: also emit directories accepted by keepDirItem (as "rel/").
	dirs        bool
	keepDirItem func(rel string) bool
	// scan runs on a worker for every kept file with the worker's reusable
	// buffer; ref is the validated directory handle (nil: open by path).
	// nil scan = list only.
	scan func(ctx context.Context, abs string, ref *dirRef, buf *[]byte) scanOut
}

// walkStats are the footer counters (P14: skips are reported, not silent),
// filled by the emitter in output order.
type walkStats struct {
	denied     int
	unreadable []string // first 3
	nUnread    int
}

func (s *walkStats) add(e *Engine, n *dirNode) {
	s.denied += n.denied
	if n.unread {
		s.nUnread++
		if len(s.unreadable) < 3 {
			s.unreadable = append(s.unreadable, e.rel(n.abs))
		}
	}
}

// footer writes the "skipped:" lines; denied and unread are per-file scan
// skips added to the walk counters (s may be nil).
func (s *walkStats) footer(b *strings.Builder, denied, unread int) {
	var paths []string
	if s != nil {
		denied, unread, paths = denied+s.denied, unread+s.nUnread, s.unreadable
	}
	if unread > 0 {
		fmt.Fprintf(b, "skipped: %d unreadable %s\n", unread, strings.Join(paths, " "))
	}
	if denied > 0 {
		fmt.Fprintf(b, "skipped: %d denied\n", denied)
	}
}

// fileJob is one file in DFS order; out receives its scan result. A job
// with stat set carries a directory's skip counts instead, so footers only
// count what the ordered output reached (CT-22). dir is the validated
// directory handle the file is opened through (nil: absolute open).
type fileJob struct {
	abs, rel string
	name     string
	out      chan scanOut // capacity 1
	stat     *dirNode
	ref      *dirRef
}

type dirNode struct {
	abs, rel string // rel to walk root, "" for the root
	top      string // matching root for ignore patterns (repo or session root)
	prefix   string // abs relative to top, slash form with trailing '/'
	st       ignoreStack
	done     chan struct{} //damnbot:unbuffered closed once the entries are read
	entries  []dirEntry
	denied   int                    // entries matching deny_paths
	unread   bool                   // the directory itself could not be listed
	ref      atomic.Pointer[dirRef] // published by readDir before done closes
	comps    []string               // this directory's components + one slot per entry
	deny     *sandbox.DenyBelow     // deny patterns that can match below this directory
}

// dirRef shares one opened directory handle between the traverser and the
// file jobs of that directory; the last release closes the handle.
type dirRef struct {
	d    *sandbox.Dir
	w    *walker
	refs atomic.Int32
}

// acquire adds one reference to the handle.
func (r *dirRef) acquire() { r.refs.Add(1) }

// release drops one reference; the last one closes the handle.
func (r *dirRef) release() {
	if r.refs.Add(-1) == 0 {
		r.w.releaseDir(r.d)
	}
}

type dirEntry struct {
	name  string
	child *dirNode // nil for a file
}

// walker shares state between the traverser, workers and emitter.
type walker struct {
	e     *Engine
	spec  *walkSpec
	stats *walkStats
	jobs  chan *fileJob
	dirs  dirQueue
	// open tracks every directory handle this walk opened, so a cancelled
	// or early-stopped walk closes what queued jobs never scanned.
	openMu sync.Mutex
	open   map[*sandbox.Dir]struct{}
}

// trackDir registers an opened directory handle.
func (w *walker) trackDir(d *sandbox.Dir) {
	w.openMu.Lock()
	w.open[d] = struct{}{}
	w.openMu.Unlock()
}

// releaseDir closes a directory handle once every job that may use it is
// done (dirNode.release) and drops it from the registry.
func (w *walker) releaseDir(d *sandbox.Dir) {
	w.openMu.Lock()
	delete(w.open, d)
	w.openMu.Unlock()
	_ = d.Close()
}

// closeDirs closes handles a cancelled walk left queued.
func (w *walker) closeDirs() {
	w.openMu.Lock()
	left := make([]*sandbox.Dir, 0, len(w.open))
	for d := range w.open {
		left = append(left, d)
	}
	w.open = map[*sandbox.Dir]struct{}{}
	w.openMu.Unlock()
	for _, d := range left {
		_ = d.Close()
	}
}

// releaseRef drops the traverser's reference to the handle.
func (n *dirNode) releaseRef() {
	if r := n.ref.Load(); r != nil {
		r.release()
	}
}

// releaseRef drops the job's reference to its directory handle.
func (j *fileJob) releaseRef() {
	if j.ref != nil {
		j.ref.release()
	}
}

// dirQueue is an unbounded LIFO of directories to prefetch.
type dirQueue struct {
	mu     sync.Mutex
	stack  []*dirNode
	signal chan struct{} // capacity 1: "queue may be non-empty"
}

func (q *dirQueue) push(ns []*dirNode) {
	q.mu.Lock()
	for _, n := range slices.Backward(ns) { // first child on top
		q.stack = append(q.stack, n)
	}
	q.mu.Unlock()
	q.wake()
}

func (q *dirQueue) wake() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *dirQueue) pop() *dirNode {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.stack) == 0 {
		return nil
	}
	n := q.stack[len(q.stack)-1]
	q.stack = q.stack[:len(q.stack)-1]
	if len(q.stack) > 0 {
		q.wake()
	}
	return n
}

// walk lists spec.root in deterministic DFS order (names byte-sorted per
// directory) while workers read directories and scan files in parallel.
// emit sees files in that order and returns false to stop early; the
// result never depends on GOMAXPROCS or timing (CT-22).
func (e *Engine) walk(parent context.Context, spec *walkSpec, st ignoreStack, top string, emit func(*fileJob, scanOut) bool) (*walkStats, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	n := runtime.GOMAXPROCS(0)
	window := 64 * n
	w := &walker{e: e, spec: spec, stats: &walkStats{}, jobs: make(chan *fileJob, window), dirs: dirQueue{signal: make(chan struct{}, 1)}, open: map[*sandbox.Dir]struct{}{}}
	defer w.closeDirs()
	order := make(chan *fileJob, window)
	prefix, err := filepath.Rel(top, spec.root)
	if err != nil {
		return nil, fmt.Errorf("codetools: walk: %w", err)
	}
	root := &dirNode{abs: spec.root, top: top, st: st, done: make(chan struct{})}
	if prefix != "." {
		root.prefix = filepath.ToSlash(prefix) + "/"
	}
	w.dirs.push([]*dirNode{root})
	g, gctx := boundary.WithContext(ctx)
	g.SetLimit(n + 1)
	g.Go(gctx, "codetools.walk.traverse", func(ctx context.Context) error {
		defer close(order)
		defer close(w.jobs)
		return w.traverse(ctx, root, order)
	})
	for range n {
		g.Go(gctx, "codetools.walk.worker", w.work)
	}
	for j := range order {
		if j.stat != nil {
			w.stats.add(e, j.stat)
			continue
		}
		var r scanOut
		select {
		case r = <-j.out:
		case <-gctx.Done():
		}
		if gctx.Err() != nil || !emit(j, r) {
			cancel()
			break
		}
	}
	for range order { // drain so the traverser can exit
	}
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return w.stats, fmt.Errorf("codetools: walk: %w", err)
	}
	if err := parent.Err(); err != nil {
		return w.stats, fmt.Errorf("codetools: walk: %w", err)
	}
	return w.stats, nil
}

// traverse visits nodes depth-first in sorted order and enqueues files.
func (w *walker) traverse(ctx context.Context, n *dirNode, order chan<- *fileJob) error {
	defer n.releaseRef() // the traverser's own reference
	select {
	case <-n.done:
	case <-ctx.Done():
		return fmt.Errorf("codetools: walk: %w", ctx.Err())
	}
	if n.denied > 0 || n.unread {
		if err := send(ctx, order, &fileJob{stat: n}); err != nil {
			return err
		}
	}
	for _, en := range n.entries {
		if en.child != nil {
			if err := w.traverse(ctx, en.child, order); err != nil {
				return err
			}
			continue
		}
		if err := w.file(ctx, n, en.name, order); err != nil {
			return err
		}
	}
	return nil
}

// file queues one file in output order and, when scanning, for a worker.
func (w *walker) file(ctx context.Context, n *dirNode, name string, order chan<- *fileJob) error {
	j := &fileJob{abs: filepath.Join(n.abs, name), rel: n.rel + name, name: name, out: make(chan scanOut, 1), ref: n.ref.Load()}
	if j.ref != nil {
		j.ref.acquire()
	}
	if w.spec.scan == nil {
		j.out <- scanOut{}
		return send(ctx, order, j)
	}
	if err := send(ctx, order, j); err != nil {
		j.releaseRef()
		return err
	}
	if err := send(ctx, w.jobs, j); err != nil {
		j.releaseRef()
		return err
	}
	return nil
}

func send(ctx context.Context, ch chan<- *fileJob, j *fileJob) error {
	select {
	case ch <- j:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("codetools: walk: %w", ctx.Err())
	}
}

// work serves scan jobs first (they gate output), then directory reads.
func (w *walker) work(ctx context.Context) error {
	var buf []byte
	scan := func(j *fileJob) {
		j.out <- w.spec.scan(ctx, j.abs, j.ref, &buf)
		j.releaseRef()
	}
	for {
		select {
		case j, ok := <-w.jobs:
			if !ok {
				return nil
			}
			scan(j)
			continue
		default:
		}
		if d := w.dirs.pop(); d != nil {
			w.readDir(ctx, d)
			continue
		}
		select {
		case j, ok := <-w.jobs:
			if !ok {
				return nil
			}
			scan(j)
		case <-w.dirs.signal:
		case <-ctx.Done():
			return nil
		}
	}
}

// readDir lists one directory through the sandbox and applies the
// hidden/ignore/deny/prune rules of CODE-TOOLS §2.3 and §8.
func (w *walker) readDir(ctx context.Context, n *dirNode) {
	defer close(n.done)
	ents, ok := w.lockDir(ctx, n)
	if !ok {
		return
	}
	st, top, prefix := w.prepareDir(ctx, n, ents)
	var children []*dirNode
	for _, de := range ents {
		if c := w.entry(n, de, st, top, prefix); c != nil {
			children = append(children, c)
		}
	}
	w.dirs.push(children)
}

// lockDir opens n through the sandbox, lists its entries and sorts them.
// It reports false when the directory could not be listed; n.unread is set
// for every failure other than cancellation.
func (w *walker) lockDir(ctx context.Context, n *dirNode) ([]os.DirEntry, bool) {
	var dir *sandbox.Dir
	var f *os.File
	var err error
	if w.spec.scan != nil && !w.spec.approved {
		// Keep the validated handle: the files of this directory open
		// through openat instead of resolving their full path again.
		dir, err = w.e.sb.OpenDir(ctx, n.abs, false)
		if dir != nil {
			f = dir.File()
		}
	} else {
		f, _, err = w.e.sb.OpenResolved(ctx, n.abs, w.spec.approved)
	}
	if err != nil || f == nil {
		if dir != nil {
			_ = dir.Close()
		}
		n.unread = ctx.Err() == nil
		return nil, false
	}
	ents, err := f.ReadDir(-1)
	if err != nil {
		if dir != nil {
			_ = dir.Close()
		} else {
			_ = f.Close()
		}
		n.unread = true
		return nil, false
	}
	if dir != nil {
		r := &dirRef{d: dir, w: w}
		r.refs.Store(1) // the traverser's reference
		n.ref.Store(r)
		w.trackDir(dir)
	} else {
		_ = f.Close()
	}
	slices.SortFunc(ents, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return ents, true
}

// prepareDir derives the ignore scope of n from its listed entries: a
// nested repository resets the scope, and ignore files push their rules.
func (w *walker) prepareDir(ctx context.Context, n *dirNode, ents []os.DirEntry) (ignoreStack, string, string) {
	st, top, prefix := n.st, n.top, n.prefix
	if _, ok := entryType(ents, ".git"); n.rel != "" && ok { // nested repository: reset scope
		top, prefix = n.abs, ""
		st = w.e.repoBase(ctx, n.abs)
	}
	// Split the directory once and keep only the deny patterns that can
	// match below it: entries are checked without joining and splitting a
	// path each time (ADR-103).
	dirComps := sandbox.PathComponents(n.abs)
	n.comps = make([]string, len(dirComps)+1)
	copy(n.comps, dirComps)
	n.deny = w.e.sb.DenyBelow(dirComps)
	for _, name := range ignoreFiles() {
		if t, ok := entryType(ents, name); ok && t.IsRegular() { // never a FIFO (CT27-4)
			st = st.push(w.e.loadIgnore(ctx, filepath.Join(n.abs, name), prefix, false))
		}
	}
	return st, top, prefix
}

// entry applies the CODE-TOOLS §2.3/§8 rules to one directory entry and
// records it on n; a kept subdirectory is returned for prefetching.
func (w *walker) entry(n *dirNode, de os.DirEntry, st ignoreStack, top, prefix string) *dirNode {
	name, typ := de.Name(), de.Type()
	isDir := typ.IsDir()
	if name == ".git" || !w.spec.hidden && name[0] == '.' || !isDir && !typ.IsRegular() {
		return nil // never .git; hidden by default; symlinks are not followed
	}
	if st.ignored(prefix+name, isDir) {
		return nil
	}
	if n.deny != nil {
		n.comps[len(n.comps)-1] = name
		if n.deny.Denied(n.comps) {
			n.denied++
			return nil
		}
	}
	if !isDir {
		w.entryFile(n, name)
		return nil
	}
	return w.entryDir(n, name, st, top, prefix)
}

// entryFile records a regular file on n.
func (w *walker) entryFile(n *dirNode, name string) {
	if w.spec.scan != nil && w.keepsFile(n.rel+name) {
		n.entries = append(n.entries, dirEntry{name: name})
		return
	}
	if w.keepsFile(n.rel + name) {
		n.entries = append(n.entries, dirEntry{name: name})
	}
}

// entryDir applies the prune rules to a subdirectory and records it on n;
// the node is returned so the walker can prefetch it.
func (w *walker) entryDir(n *dirNode, name string, st ignoreStack, top, prefix string) *dirNode {
	if w.spec.keepDir != nil && !w.spec.keepDir(n.rel+name) {
		return nil
	}
	if w.spec.dirs && w.spec.keepDirItem(n.rel+name) {
		n.entries = append(n.entries, dirEntry{name: name + "/"})
	}
	c := &dirNode{abs: filepath.Join(n.abs, name), rel: n.rel + name + "/", top: top, prefix: prefix + name + "/", st: st, done: make(chan struct{})}
	n.entries = append(n.entries, dirEntry{name: name, child: c})
	return c
}

// keepsFile reports whether the spec's file filter accepts rel.
func (w *walker) keepsFile(rel string) bool {
	return w.spec.keepFile == nil || w.spec.keepFile(rel)
}

// entryType is the listed type of name in the sorted ents.
func entryType(ents []os.DirEntry, name string) (os.FileMode, bool) {
	i, ok := slices.BinarySearchFunc(ents, name, func(d os.DirEntry, n string) int { return strings.Compare(d.Name(), n) })
	if !ok {
		return 0, false
	}
	return ents[i].Type(), true
}
