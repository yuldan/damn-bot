package codetools

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// writeMask matches sandbox's write flags: an open with any of them writes.
const writeMask = os.O_WRONLY | os.O_RDWR | os.O_CREATE | os.O_TRUNC | os.O_APPEND

// ClientFS is the ACP client filesystem seam (SPEC §13.3, GAP-UX-01). It
// returns the client's text buffer for a resolved path — unsaved editor
// changes included — and writes new bytes back to it. The engine keeps the
// tag rules, editing, sandbox, approval and journal; only the bytes move.
//
// A capability the client did not advertise is reported as unsupported, not
// as an error: the engine falls back to disk and reports it through
// OnClientFallback. notFound means the client has no buffer for path.
type ClientFS interface {
	// Supports reports whether the client advertised the capability for
	// this direction. A write needs read+write (tags come from the buffer).
	Supports(write bool) bool
	ReadTextFile(ctx context.Context, path string) (data []byte, notFound bool, err error)
	WriteTextFile(ctx context.Context, path string, data []byte) error
}

// openClient serves an open from the client buffer: the bytes are
// materialised in a private temp file so Read, openCurrent and Edit keep
// using their existing tag/encoding/edit machinery. handled=false means the
// caller must open from disk (capability absent).
func (e *Engine) openClient(ctx context.Context, p, abs string, flag int, o Opts) (*target, error) {
	op := sandbox.OpRead
	if flag&writeMask != 0 {
		op = sandbox.OpWrite
	}
	v, resolved, cerr := e.sb.Check(ctx, abs, op)
	if cerr != nil {
		return nil, mapOpenErr(p, cerr)
	}
	if v != sandbox.Inside {
		if aerr := approval(v, p, o.Approved); aerr != nil {
			return nil, aerr
		}
	}
	data, notFound, rerr := e.client.ReadTextFile(ctx, resolved)
	switch {
	case notFound:
		return nil, mapOpenErr(p, fs.ErrNotExist)
	case rerr != nil:
		return nil, ioErr("client read", p, rerr)
	}
	f, terr := os.CreateTemp("", "damnbot-acp-*")
	if terr != nil {
		return nil, ioErr("client buffer", p, terr)
	}
	if _, werr := f.Write(data); werr != nil {
		return nil, ioErr("client buffer", p, errors.Join(werr, f.Close(), os.Remove(f.Name())))
	}
	if _, serr := f.Seek(0, io.SeekStart); serr != nil {
		return nil, ioErr("client buffer", p, errors.Join(serr, f.Close(), os.Remove(f.Name())))
	}
	return &target{
		f: f, tmp: f.Name(), resolved: resolved, rel: e.rel(resolved), outside: v != sandbox.Inside,
		journal: o.Journal, client: e.client, bufSum: sha256.Sum256(data),
	}, nil
}

// commitClient writes through the client buffer. The buffer is re-read
// first: a change since it was opened is stale_tag, never an overwrite.
func (e *Engine) commitClient(ctx context.Context, t *target, mode fs.FileMode, data []byte) (ci commitInfo, err error) {
	ci = commitInfo{ownerOK: true, modeOK: true, xattrOK: true, sum: sha256.Sum256(data), key: fileKey{path: t.resolved, size: int64(len(data))}}
	after, terr := os.CreateTemp("", "damnbot-acp-after-*")
	if terr != nil {
		return ci, ioErr("client buffer", t.rel, terr)
	}
	defer func() { err = errors.Join(err, after.Close(), os.Remove(after.Name())) }()
	if _, err := after.Write(data); err != nil {
		return ci, ioErr("client buffer", t.rel, err)
	}
	if err := after.Sync(); err != nil {
		return ci, ioErr("client buffer", t.rel, err)
	}
	if _, err := after.Seek(0, io.SeekStart); err != nil {
		return ci, ioErr("client buffer", t.rel, err)
	}
	if t.journal != nil {
		if jerr := t.journal(ctx, &Commit{Path: t.resolved, Before: t.f, After: after, Mode: mode}); jerr != nil {
			return ci, ioErr("journal", t.rel, jerr)
		}
	}
	cur, notFound, rerr := t.client.ReadTextFile(ctx, t.resolved)
	if notFound && t.f != nil {
		return ci, toolErr(CodeStaleRead, "read_file returns the current tag", "%s: stale_tag; the client buffer is gone", t.rel)
	}
	if rerr != nil {
		return ci, ioErr("client read", t.rel, rerr)
	}
	if !notFound && sha256.Sum256(cur) != t.bufSum {
		return ci, toolErr(CodeStaleRead, "read_file returns the current tag", "%s: stale_tag; the client buffer changed before the write", t.rel)
	}
	if werr := t.client.WriteTextFile(ctx, t.resolved, data); werr != nil {
		return ci, ioErr("client write", t.rel, werr)
	}
	return ci, nil
}

// sourceLocal decides one call's source: when the client lacks the
// capability for this direction it reports the fallback and returns true so
// the caller opens from disk.
func (e *Engine) sourceLocal(ctx context.Context, write bool) (bool, error) {
	if e.client == nil || e.client.Supports(write) {
		return false, nil
	}
	if e.fallback == nil {
		return true, nil
	}
	return true, e.fallback(ctx, write)
}
