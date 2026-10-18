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

	"github.com/damnbot/damnbot/internal/boundary"
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
	// buffer; nil = list only.
	scan func(ctx context.Context, abs string, buf *[]byte) scanOut
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
// count what the ordered output reached (CT-22).
type fileJob struct {
	abs, rel string
	out      chan scanOut // capacity 1
	stat     *dirNode
}

type dirNode struct {
	abs, rel string // rel to walk root, "" for the root
	top      string // matching root for ignore patterns (repo or session root)
	prefix   string // abs relative to top, slash form with trailing '/'
	st       ignoreStack
	done     chan struct{} //damnbot:unbuffered closed once the entries are read
	entries  []dirEntry
	denied   int  // entries matching deny_paths
	unread   bool // the directory itself could not be listed
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
	w := &walker{e: e, spec: spec, stats: &walkStats{}, jobs: make(chan *fileJob, window), dirs: dirQueue{signal: make(chan struct{}, 1)}}
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
	j := &fileJob{abs: filepath.Join(n.abs, name), rel: n.rel + name, out: make(chan scanOut, 1)}
	if w.spec.scan == nil {
		j.out <- scanOut{}
		return send(ctx, order, j)
	}
	if err := send(ctx, order, j); err != nil {
		return err
	}
	return send(ctx, w.jobs, j)
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
	for {
		select {
		case j, ok := <-w.jobs:
			if !ok {
				return nil
			}
			j.out <- w.spec.scan(ctx, j.abs, &buf)
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
			j.out <- w.spec.scan(ctx, j.abs, &buf)
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
	f, _, err := w.e.sb.OpenResolved(ctx, n.abs, w.spec.approved)
	if err != nil || f == nil {
		n.unread = ctx.Err() == nil
		return
	}
	ents, err := f.ReadDir(-1)
	if cerr := f.Close(); err != nil || cerr != nil {
		n.unread = true
		return
	}
	slices.SortFunc(ents, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	st, top, prefix := n.st, n.top, n.prefix
	if n.rel != "" && hasEntry(ents, ".git") { // nested repository: reset scope
		top, prefix = n.abs, ""
		st = w.e.repoBase(ctx, n.abs)
	}
	for _, name := range ignoreFiles() {
		if hasEntry(ents, name) {
			st = st.push(w.e.loadIgnore(ctx, filepath.Join(n.abs, name), prefix))
		}
	}
	var children []*dirNode
	for _, de := range ents {
		if c := w.entry(n, de, st, top, prefix); c != nil {
			children = append(children, c)
		}
	}
	w.dirs.push(children)
}

// entry applies the CODE-TOOLS §2.3/§8 rules to one directory entry and
// records it on n; a kept subdirectory is returned for prefetching.
func (w *walker) entry(n *dirNode, de os.DirEntry, st ignoreStack, top, prefix string) *dirNode {
	name, typ := de.Name(), de.Type()
	isDir := typ.IsDir()
	switch {
	case name == ".git", !w.spec.hidden && name[0] == '.', !isDir && !typ.IsRegular():
		return nil // never .git; hidden by default; symlinks are not followed
	case st.ignored(prefix+name, isDir):
		return nil
	case !isDir && w.spec.scan != nil && (w.spec.keepFile == nil || w.spec.keepFile(n.rel+name)):
		// scanFile opens through OpenResolved, which enforces deny_paths
		// and reports skipDenied: one deny match per file instead of two.
		n.entries = append(n.entries, dirEntry{name: name})
		return nil
	case w.e.sb.Denied(filepath.Join(n.abs, name)):
		n.denied++
		return nil
	case !isDir:
		if w.spec.keepFile == nil || w.spec.keepFile(n.rel+name) {
			n.entries = append(n.entries, dirEntry{name: name})
		}
		return nil
	case w.spec.keepDir != nil && !w.spec.keepDir(n.rel+name):
		return nil
	}
	if w.spec.dirs && w.spec.keepDirItem(n.rel+name) {
		n.entries = append(n.entries, dirEntry{name: name + "/"})
	}
	c := &dirNode{abs: filepath.Join(n.abs, name), rel: n.rel + name + "/", top: top, prefix: prefix + name + "/", st: st, done: make(chan struct{})}
	n.entries = append(n.entries, dirEntry{name: name, child: c})
	return c
}

func hasEntry(ents []os.DirEntry, name string) bool {
	_, ok := slices.BinarySearchFunc(ents, name, func(d os.DirEntry, n string) int { return strings.Compare(d.Name(), n) })
	return ok
}

// repoBase is the ignore stack at the root of a (nested) repository.
func (e *Engine) repoBase(ctx context.Context, repo string) ignoreStack {
	var st ignoreStack
	st = st.push(e.loadIgnore(ctx, e.gitConfigExcludes(ctx, repo), ""))
	return st.push(e.loadIgnore(ctx, filepath.Join(repo, ".git", "info", "exclude"), ""))
}
