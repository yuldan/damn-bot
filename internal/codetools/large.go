package codetools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strconv"
	"time"
	"unicode/utf8"
)

// lineIndex holds the byte offset of every checkpointLines-th line start
// (lines 1, 1001, 2001, …) and the line total (CODE-TOOLS §4.5).
type lineIndex struct {
	offs  []int64
	total int
}

// indexFile builds and caches the line index of a large file. When its
// mtime is racy the same pass hashes the content and attaches the digest
// to the file's stat-tag, so a later edit can prove the content unchanged
// (CODE-TOOLS §5.5: "hash yang dihitung indeks").
func (e *Engine) indexFile(ctx context.Context, f io.ReaderAt, resolved string, key fileKey, racy bool) (*lineIndex, error) {
	var h hash.Hash
	if racy {
		h = sha256.New()
	}
	idx, err := buildIndex(ctx, f, key.size, h)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache.index.put(key, idx, 8*len(idx.offs)+64)
	if ent, ok := e.cache.tags[tagKey(resolved, statTag(key))]; ok && h != nil && ent.stat == key {
		copy(ent.full[:], h.Sum(nil))
		ent.hasFull = true
		e.cache.tags[tagKey(resolved, statTag(key))] = ent
	}
	return idx, nil
}

// buildIndex scans r (size bytes) once, counting with bytes.Count and only
// locating newlines that start a checkpoint; h (optional) hashes the bytes.
func buildIndex(ctx context.Context, r io.ReaderAt, size int64, h hash.Hash) (*lineIndex, error) {
	idx := &lineIndex{offs: []int64{0}}
	buf := make([]byte, 1<<20)
	var last byte
	for pos := int64(0); pos < size; {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("codetools: index: %w", err)
		}
		n, err := r.ReadAt(buf[:min(int64(len(buf)), size-pos)], pos)
		if n == 0 && err != nil {
			return nil, fmt.Errorf("codetools: index: %w", err)
		}
		idx.scan(buf[:n], pos)
		if h != nil {
			h.Write(buf[:n])
		}
		last = buf[n-1]
		pos += int64(n)
	}
	if size > 0 && last != '\n' {
		idx.total++
	}
	if want := (idx.total-1)/checkpointLines + 1; idx.total > 0 && len(idx.offs) > want {
		idx.offs = idx.offs[:want] // drop a checkpoint at EOF
	}
	return idx, nil
}

// scan counts the newlines of chunk (at file offset pos) into total and
// records each line start that is a checkpoint.
func (idx *lineIndex) scan(chunk []byte, pos int64) {
	next := len(idx.offs) * checkpointLines
	if c := bytes.Count(chunk, []byte{'\n'}); idx.total+c < next {
		idx.total += c
		return
	}
	for i, b := range chunk {
		if b != '\n' {
			continue
		}
		if idx.total++; idx.total == next {
			idx.offs = append(idx.offs, pos+int64(i)+1)
			next += checkpointLines
		}
	}
}

// readLarge pages a UTF-8 file above fullHashMax without loading it.
func (e *Engine) readLarge(ctx context.Context, f *fileView, off, limit int, o Opts) (Result, error) {
	tag := statTag(f.key)
	ent := &tagEntry{sum: statSum(f.key), stat: f.key, at: time.Now()}
	e.remember(f.t.resolved, tag, ent, nil)
	e.mu.Lock()
	idx, ok := e.cache.index.get(f.key)
	e.mu.Unlock()
	if !ok && off < 0 {
		e.indexAsync(ctx, f)
		return e.tail(f, tag, -off, limit, o)
	}
	if !ok {
		var err error
		if idx, err = e.indexFile(ctx, f.t.f, f.t.resolved, f.key, ent.racy()); err != nil {
			return Result{}, &ToolError{Code: CodeInternal, Message: "index " + f.t.rel, Err: err}
		}
	}
	start := off
	if off < 0 {
		start = max(1, idx.total+off+1)
	}
	if start > idx.total {
		return Result{}, toolErr(CodeInvalidArgs, "", "/offset: %d is past the end (%d lines)", off, idx.total)
	}
	pos := idx.offs[(start-1)/checkpointLines]
	br := bufio.NewReaderSize(io.NewSectionReader(f.t.f, pos, f.key.size-pos), tailChunk)
	for range (start - 1) % checkpointLines {
		if _, _, err := readLine(br); err != nil {
			return Result{}, &ToolError{Code: CodeInternal, Message: "read " + f.t.rel, Err: err}
		}
	}
	pw := newPage(o.pageChars())
	for n := start; n <= idx.total && n < start+limit; n++ {
		line, extra, err := readLine(br)
		if err != nil && !errors.Is(err, io.EOF) {
			return Result{}, &ToolError{Code: CodeInternal, Message: "read " + f.t.rel, Err: err}
		}
		if !pw.addCut(strconv.Itoa(n), trimEOL(line), extra) {
			break
		}
	}
	next := 0
	if last := start + pw.count - 1; last < idx.total {
		next = last + 1
	}
	hdr := header{rel: f.t.rel, tag: tag, first: start, count: pw.count, total: idx.total, enc: f.enc, eol: eolLabel(f.head)}
	return pw.result(e, f.t, &hdr, next), nil
}

// racy reports an entry whose file mtime is within racyWindow of the tag.
func (t *tagEntry) racy() bool { return !time.Unix(0, t.stat.mtimeNs).Add(racyWindow).Before(t.at) }

func hashFile(f *os.File, size int64) ([32]byte, error) {
	h := sha256.New()
	var sum [32]byte
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, size)); err != nil {
		return sum, fmt.Errorf("codetools: hash: %w", err)
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// readLine returns the next line, keeping at most enough bytes for
// lineMaxChars runes; extra counts the runes dropped beyond that.
func readLine(br *bufio.Reader) (line []byte, extra int, err error) {
	const keep = lineMaxChars*utf8.UTFMax + 2
	for {
		chunk, err := br.ReadSlice('\n')
		if room := keep - len(line); room > 0 {
			take := min(room, len(chunk))
			line = append(line, chunk[:take]...)
			chunk = chunk[take:]
		}
		extra += utf8.RuneCount(chunk)
		switch {
		case err == nil:
			return line, extra, nil
		case !errors.Is(err, bufio.ErrBufferFull):
			return line, extra, fmt.Errorf("codetools: read line: %w", err)
		}
	}
}

// tail reads the last n lines backwards in tailChunk steps (CODE-TOOLS §4.5)
// and numbers them from the end while no line index exists.
func (e *Engine) tail(f *fileView, tag string, n, limit int, o Opts) (Result, error) {
	var buf []byte
	pos := f.key.size
	for pos > 0 {
		step := min(int64(tailChunk), pos)
		pos -= step
		chunk := make([]byte, step, int64(len(buf))+step)
		if _, err := f.t.f.ReadAt(chunk, pos); err != nil && !errors.Is(err, io.EOF) {
			return Result{}, &ToolError{Code: CodeInternal, Message: "read " + f.t.rel, Err: err}
		}
		buf = append(chunk, buf...)
		if bytes.Count(bytes.TrimSuffix(buf, []byte{'\n'}), []byte{'\n'}) >= n {
			break
		}
	}
	lines := splitLines(buf)
	if pos > 0 {
		lines = lines[1:] // first piece may be a partial line
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	pw := newPage(o.pageChars())
	for i, l := range lines[:min(limit, len(lines))] {
		if !pw.add(strconv.Itoa(i-len(lines)), trimEOL(l)) {
			break
		}
	}
	hdr := header{rel: f.t.rel, tag: tag, first: -len(lines), count: pw.count, enc: f.enc, eol: eolLabel(f.head), fromEnd: true}
	next := 0
	if pw.count < len(lines) {
		next = pw.count - len(lines)
	}
	return pw.result(e, f.t, &hdr, next), nil
}

// indexAsync builds the line index of a large file on an Engine-owned
// goroutine (CODE-TOOLS §4.5); a file changed meanwhile is not cached.
func (e *Engine) indexAsync(ctx context.Context, f *fileView) {
	if f.t.outside {
		return
	}
	e.mu.Lock()
	if e.building[f.key] {
		e.mu.Unlock()
		return
	}
	e.building[f.key] = true
	e.mu.Unlock()
	key, path := f.key, f.t.resolved
	racy := (&tagEntry{stat: key, at: time.Now()}).racy()
	e.goBackground(ctx, "codetools.index", func(ctx context.Context) (err error) {
		defer func() {
			e.mu.Lock()
			delete(e.building, key)
			e.mu.Unlock()
		}()
		fh, _, err := e.sb.OpenFile(ctx, path, os.O_RDONLY, 0)
		if err != nil || fh == nil {
			return fmt.Errorf("codetools: index open: %w", err)
		}
		defer closeInto(&err, fh)
		fi, err := fh.Stat()
		if err != nil {
			return fmt.Errorf("codetools: index stat: %w", err)
		}
		if keyOf(path, fi) != key {
			return nil // changed since the read: a later read rebuilds
		}
		_, err = e.indexFile(ctx, fh, path, key, racy)
		return err
	})
}
