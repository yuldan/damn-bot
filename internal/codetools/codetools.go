// Package codetools implements the eager file tools of CODE-TOOLS.md:
// read_file, grep, glob, edit_file and write_file (ADR-014, ADR-031,
// ADR-103). Every path goes through internal/sandbox before any I/O; tools
// return compact text (CODE-TOOLS §9) plus machine facts in Meta, and fail
// closed with a *ToolError carrying a SPEC §4.3 error code.
//
// One Engine belongs to one session: it owns the snapshot tags handed to
// the model and the bounded caches of CODE-TOOLS §7.
package codetools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/sandbox"
)

// Code is a tool error code from the closed SPEC §4.3 list.
type Code string

// Error codes produced by this package (subset of SPEC §4.3).
const (
	CodeInvalidArgs       Code = "invalid_args"
	CodeNotFound          Code = "not_found"
	CodeOutsideWorkspace  Code = "outside_workspace"
	CodeDeniedPath        Code = "denied_path"
	CodeStaleRead         Code = "stale_read"
	CodeNoMatch           Code = "no_match"
	CodeAmbiguousMatch    Code = "ambiguous_match"
	CodeTooLarge          Code = "too_large"
	CodeDiskFull          Code = "disk_full"
	CodeWriteVerifyFailed Code = "write_verify_failed"
	CodeInternal          Code = "internal"
)

// ToolError is the structured failure returned to the model
// ({"ok":false,"error":{code,message,hint}}, SPEC §4.3).
type ToolError struct {
	Code    Code
	Message string
	Hint    string
	Err     error // cause, never shown to the model
}

func (e *ToolError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("codetools: %s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("codetools: %s: %s", e.Code, e.Message)
}

func (e *ToolError) Unwrap() error { return e.Err }

// codeOf is the tool error code of err: "" for nil, CodeInternal for an
// error that is not a *ToolError.
func codeOf(err error) Code {
	if err == nil {
		return ""
	}
	if te, ok := errors.AsType[*ToolError](err); ok {
		return te.Code
	}
	return CodeInternal
}

func toolErr(c Code, hint, format string, args ...any) *ToolError {
	return &ToolError{Code: c, Message: fmt.Sprintf(format, args...), Hint: hint}
}

// Result is a successful tool result: Content is the model-facing text
// before redaction (SPEC §4.3 langkah 10), Meta the machine facts.
type Result struct {
	Content string
	Meta    map[string]any
}

// Opts are per-call facts decided by the dispatch pipeline.
type Opts struct {
	// Approved: the user approved an outside_root verdict for this call
	// (SPEC §4.3 langkah 7). Without it outside_root is outside_workspace.
	Approved bool
	// PageChars caps paged output (ADR-031): 0 = DefaultPageChars; the
	// pipeline passes ReducedPageChars once the turn aggregate is spent.
	PageChars int
	// Journal, when set, is called once per committing write after the new
	// bytes are durable in the temp file and before the target is replaced
	// (SPEC §4.3 langkah 8, §18.1); an error aborts the write with the
	// target untouched.
	Journal func(ctx context.Context, c *Commit) error
	// local forces one call to the disk when the client lacks the
	// capability for this direction (GAP-UX-01).
	local bool
}

// Commit is one file replacement about to happen. Before is the current
// file (nil when the write creates it) and After the fsynced temp holding
// the new bytes; readers use ReadAt (offsets are arbitrary). Mode is the
// mode both sides keep; 0 on creation, where After's own mode is final.
// The files are only valid during the Journal call.
type Commit struct {
	Path          string // resolved target path
	Before, After *os.File
	Mode          fs.FileMode
}

// Page caps of ADR-031 / CODE-TOOLS §1 butir 6.
const (
	DefaultPageChars = 16000
	ReducedPageChars = 4000
)

func (o Opts) pageChars() int { return cmp.Or(max(o.PageChars, 0), DefaultPageChars) }

// Config configures one session Engine.
type Config struct {
	// Sandbox is the session path sandbox (required).
	Sandbox *sandbox.Sandbox
	// Roots are the session roots in sandbox order: Roots[0] is the cwd that
	// relative paths resolve against; Roots[n>0] display as "@n/".
	Roots []string
	// NewEOL is files.new_eol for created files: "lf" (default) or "crlf".
	NewEOL string
	// Home and XDGConfigHome locate the global git config and excludes
	// (CODE-TOOLS §2.3); empty = os.UserHomeDir / $XDG_CONFIG_HOME or
	// Home/.config.
	Home, XDGConfigHome string
	// Snapshot receives the old bytes of a hard-linked file before it is
	// rewritten in place (CODE-TOOLS §5.4: rename would break the link, so
	// a crash mid-copy is recoverable only from this snapshot); an error
	// aborts the write with the file untouched. The dispatch pipeline
	// stores it in file_snapshots (SPEC §4.3 langkah 9).
	Snapshot func(ctx context.Context, resolved string, old *os.File) error
	// ClientFS serves file bytes from the ACP client buffer (GAP-UX-01);
	// nil keeps every file tool local.
	ClientFS ClientFS
	// OnClientFallback reports a local fallback per capability; the caller
	// records acp_fs_fallback and dedupes per session.
	OnClientFallback func(ctx context.Context, write bool) error
}

// Engine runs the file tools of one session. Close releases its caches and
// stops background index builds.
type Engine struct {
	sb       *sandbox.Sandbox
	roots    []string
	newEOL   string
	fold     bool // case-insensitive root prefixes (Windows)
	home     string
	xdg      string
	snapshot func(ctx context.Context, resolved string, old *os.File) error
	client   ClientFS
	fallback func(ctx context.Context, write bool) error
	// hook is a test seam called at named commit points; an error aborts
	// the write there (failure injection, CT-25 kill points).
	hook func(point string) error

	mu       sync.Mutex
	bg       []bgTask // background index builds, stopped by Close
	building map[fileKey]bool
	idle     *time.Timer // idle cache release (CODE-TOOLS §7)
	cache    *caches
	locks    map[string]*pathMu // per-file edit locks, dropped when idle
}

// New builds an Engine; roots are canonicalized like the sandbox does.
func New(cfg *Config) (*Engine, error) {
	if cfg == nil || cfg.Sandbox == nil || len(cfg.Roots) == 0 {
		return nil, errors.New("codetools: sandbox and roots required")
	}
	e := &Engine{sb: cfg.Sandbox, newEOL: "\n", fold: runtime.GOOS == "windows", snapshot: cfg.Snapshot, client: cfg.ClientFS, fallback: cfg.OnClientFallback, building: map[fileKey]bool{}, locks: map[string]*pathMu{}}
	if cfg.NewEOL == "crlf" {
		e.newEOL = "\r\n"
	}
	e.home, e.xdg = cfg.Home, cfg.XDGConfigHome
	if h, err := os.UserHomeDir(); e.home == "" && err == nil {
		e.home = h
	}
	if e.xdg == "" {
		e.xdg = os.Getenv("XDG_CONFIG_HOME")
	}
	if e.xdg == "" && e.home != "" {
		e.xdg = filepath.Join(e.home, ".config")
	}
	for _, r := range cfg.Roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, fmt.Errorf("codetools: root: %w", err)
		}
		c, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("codetools: root: %w", err)
		}
		e.roots = append(e.roots, c)
	}
	e.cache = newCaches()
	return e, nil
}

// Close stops background work and drops every cache and tag.
func (e *Engine) Close() {
	e.mu.Lock()
	bg := e.bg
	e.bg = nil
	e.cache = newCaches()
	if e.idle != nil {
		e.idle.Stop()
	}
	e.mu.Unlock()
	for _, t := range bg {
		t.cancel()
		<-t.task.Done()
	}
}

type bgTask struct {
	task   *boundary.Task
	cancel context.CancelFunc
}

// goBackground runs fn on an Engine-owned goroutine (E-26) that outlives
// the request (ctx values kept, cancellation dropped) until Close.
func (e *Engine) goBackground(ctx context.Context, name string, fn func(context.Context) error) {
	bctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.mu.Lock()
	defer e.mu.Unlock()
	live := e.bg[:0]
	for _, t := range e.bg {
		select {
		case <-t.task.Done():
			t.cancel()
		default:
			live = append(live, t)
		}
	}
	live = append(live, bgTask{task: boundary.Go(bctx, name, fn), cancel: cancel})
	e.bg = live
}

// abs resolves a tool path argument against the session cwd.
func (e *Engine) abs(p string) string {
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		return filepath.Clean(p)
	}
	return filepath.Join(e.roots[0], p)
}

// rel renders a resolved path relative to its session root ("@n/" for
// add_dirs), or the absolute path when outside every root.
func (e *Engine) rel(resolved string) string {
	for i, r := range e.roots {
		if !hasPrefixPath(resolved, r, e.fold) {
			continue
		}
		rel := strings.TrimLeft(resolved[len(r):], `/\`)
		if rel == "" {
			rel = "."
		}
		rel = filepath.ToSlash(rel)
		if i > 0 {
			return fmt.Sprintf("@%d/%s", i, rel)
		}
		return rel
	}
	return filepath.ToSlash(resolved)
}

func hasPrefixPath(p, root string, fold bool) bool {
	if len(p) < len(root) || p[:len(root)] != root && (!fold || !strings.EqualFold(p[:len(root)], root)) {
		return false
	}
	return len(p) == len(root) || os.IsPathSeparator(p[len(root)]) || os.IsPathSeparator(root[len(root)-1])
}

// target is an opened tool path.
type target struct {
	f        *os.File
	tmp      string // client-buffer temp to remove on close
	resolved string
	rel      string
	outside  bool // opened after an approved non-Inside verdict
	client   ClientFS
	bufSum   [32]byte // client buffer digest when opened
	journal  func(ctx context.Context, c *Commit) error
}

// approval fails a verdict other than Inside (outside_root,
// hardlinked_file) that the user did not approve (SPEC §4.3 langkah 7).
func approval(v sandbox.Verdict, p string, approved bool) error {
	if v == sandbox.Inside || approved {
		return nil
	}
	return toolErr(CodeOutsideWorkspace, "needs the user's approval", "%s: %s", p, v)
}

// open opens a tool path through the sandbox (CODE-TOOLS §8). A verdict
// other than Inside needs o.Approved (else outside_workspace); deny_paths
// is denied_path.
func (e *Engine) open(ctx context.Context, p string, flag int, perm fs.FileMode, o Opts) (*target, error) {
	abs := e.abs(p)
	if e.client != nil && !o.local {
		return e.openClient(ctx, p, abs, flag, o)
	}
	f, v, err := e.openPath(ctx, abs, p, flag, perm, o.Approved)
	if err != nil {
		return nil, err
	}
	resolved := f.Name()
	if _, r, cerr := e.sb.Check(ctx, abs, sandbox.OpRead); cerr == nil {
		resolved = r
	}
	return &target{f: f, resolved: resolved, rel: e.rel(resolved), outside: v != sandbox.Inside, journal: o.Journal}, nil
}

// openPath opens abs (shown as p in errors) through the sandbox; a verdict
// other than Inside needs approved, then OpenApproved (SPEC §4.3 langkah 7).
func (e *Engine) openPath(ctx context.Context, abs, p string, flag int, perm fs.FileMode, approved bool) (*os.File, sandbox.Verdict, error) {
	f, v, err := e.sb.OpenFile(ctx, abs, flag, perm)
	if err == nil && v != sandbox.Inside {
		if aerr := approval(v, p, approved); aerr != nil {
			return nil, v, aerr
		}
		f, err = e.sb.OpenApproved(ctx, abs, flag, perm)
	}
	if err != nil {
		return nil, v, mapOpenErr(p, err)
	}
	return f, v, nil
}

// openResolved opens a path found by a walk or written by this Engine
// (sandbox.OpenResolved); a verdict needing approval that approved does
// not cover fails closed.
func (e *Engine) openResolved(ctx context.Context, p string, approved bool) (*os.File, error) {
	f, v, err := e.sb.OpenResolved(ctx, p, approved)
	switch {
	case err != nil:
		return nil, mapOpenErr(e.rel(p), err)
	case f == nil:
		return nil, approval(v, e.rel(p), false)
	}
	return f, nil
}

// mapOpenErr classifies sandbox/OS open failures by sentinel (E-21).
func mapOpenErr(p string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("codetools: %s: %w", p, err)
	case errors.Is(err, sandbox.ErrDeniedPath):
		return &ToolError{Code: CodeDeniedPath, Message: p + " matches deny_paths", Err: err}
	case errors.Is(err, sandbox.ErrOutsideWorkspace):
		return &ToolError{Code: CodeOutsideWorkspace, Message: p + " escaped the workspace while opening", Hint: "retry; the path changed under a symlink", Err: err}
	case errors.Is(err, fs.ErrNotExist):
		return &ToolError{Code: CodeNotFound, Message: p + " does not exist", Err: err}
	case errors.Is(err, sandbox.ErrInvalidPath):
		return &ToolError{Code: CodeInvalidArgs, Message: "invalid path " + p, Err: err}
	case errors.Is(err, sandbox.ErrNotRegular):
		return &ToolError{Code: CodeInvalidArgs, Message: p + " is not a regular file", Err: err}
	case errors.Is(err, fs.ErrPermission):
		return &ToolError{Code: CodeDeniedPath, Message: p + ": permission denied by the OS", Err: err}
	}
	return &ToolError{Code: CodeInternal, Message: "open " + p, Err: err}
}

// close releases the target's read handle once (idempotent).
func (t *target) close(err *error) {
	if t.f != nil {
		closeInto(err, t.f)
		t.f = nil
	}
	if t.tmp != "" {
		if rerr := os.Remove(t.tmp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err != nil {
			*err = errors.Join(*err, fmt.Errorf("codetools: remove client temp: %w", rerr))
		}
		t.tmp = ""
	}
}

// closeInto closes c and keeps its error in *err unless one is set.
func closeInto(err *error, c interface{ Close() error }) {
	if cerr := c.Close(); cerr != nil && *err == nil {
		*err = fmt.Errorf("codetools: close: %w", cerr)
	}
}
