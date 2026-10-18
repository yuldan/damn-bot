package codetools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// largeDiffCap bounds the replaced text kept for the no-op check of a streamed edit.
const largeDiffCap = 64 << 10

// lineRange is one resolved line-range edit of a streamed file.
type lineRange struct {
	idx, s, e int
	text      string
	oldBytes  int // replaced bytes; ≤ largeDiffCap are compared for the no-op check
}

// editLarge applies line-range edits to a UTF-8 file above inMemoryMax by
// streaming prefix → new text → suffix (CODE-TOOLS §5.4); string edits
// need the whole file in memory and get too_large.
func (e *Engine) editLarge(ctx context.Context, c *current, edits []EditItem) (Result, error) {
	head := make([]byte, sniffLen)
	n, err := c.t.f.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{}, ioErr("read", c.t.rel, err)
	}
	if enc := detectEnc(head[:n]); enc.enc != EncUTF8 || enc.hasBOM {
		return Result{}, toolErr(CodeTooLarge, "", "%s: only UTF-8 files above %d MiB are edited by line range", c.t.rel, inMemoryMax>>20)
	}
	idx, err := buildIndex(ctx, c.t.f, c.key.size, nil)
	if err != nil {
		return Result{}, ioErr("index", c.t.rel, err)
	}
	rs, terr := largeRanges(edits, idx.total)
	if terr != nil {
		return Result{}, terr
	}
	eol := ""
	ci, err := e.commitStream(ctx, c.t, c.key, c.fi.Mode().Perm(), func(w io.Writer) error {
		var ferr error
		eol, ferr = streamEdit(io.NewSectionReader(c.t.f, 0, c.key.size), w, rs)
		return ferr
	})
	if err != nil {
		return Result{}, err
	}
	tag := statTag(ci.key)
	e.remember(c.t.resolved, tag, &tagEntry{sum: statSum(ci.key), stat: ci.key, at: time.Now(), full: ci.sum, hasFull: true}, nil)
	add, del := 0, 0
	for _, r := range rs {
		add, del = add+len(splitDocLines(r.text, "\n")), del+r.e-r.s+1
	}
	content := fmt.Sprintf("edited %s #%s→#%s · %d edits · +%d -%d (diff omitted above %d MiB)", c.t.rel, c.tag, tag, len(rs), add, del, inMemoryMax>>20)
	meta := map[string]any{
		"tag": tag, "replacements": len(rs), "diff_stat": fmt.Sprintf("+%d -%d", add, del), "eol": eolName(eol),
		"encoding": "utf-8", "owner_preserved": ci.ownerOK, "mode_preserved": ci.modeOK, "xattr_preserved": ci.xattrOK,
	}
	return Result{Content: e.wrapOutside(c.t, content), Meta: meta}, nil
}

// largeRanges validates streamed edits: line ranges only, sorted, no
// overlap (adjacent allowed), no sentinel.
func largeRanges(edits []EditItem, total int) ([]lineRange, *ToolError) {
	var rs []lineRange
	for i := range edits {
		ed := &edits[i]
		if ed.OldString != nil || ed.NewString != nil {
			return nil, toolErr(CodeTooLarge, "use {start_line,end_line,new_text} for files above 64 MiB", "/edits/%d: string edits need the file in memory", i)
		}
		if ed.StartLine == nil || ed.EndLine == nil || ed.NewText == nil {
			return nil, toolErr(CodeInvalidArgs, "", "/edits/%d: start_line, end_line and new_text required", i)
		}
		if strings.Contains(*ed.NewText, sentinel) {
			return nil, toolErr(CodeInvalidArgs, "secret redacted; edit around it", "/edits/%d: contains a redaction sentinel", i)
		}
		s, e := *ed.StartLine, *ed.EndLine
		if s < 0 && e < 0 {
			s, e = total+s+1, total+e+1
		}
		if s < 1 || e < s || e > total {
			return nil, toolErr(CodeInvalidArgs, "", "/edits/%d: line range %d..%d outside 1..%d", i, s, e, total)
		}
		rs = append(rs, lineRange{idx: i, s: s, e: e, text: *ed.NewText})
	}
	slices.SortFunc(rs, func(a, b lineRange) int { return a.s - b.s })
	for k := 1; k < len(rs); k++ {
		if rs[k].s <= rs[k-1].e {
			return nil, toolErr(CodeInvalidArgs, "", "edits %d and %d overlap at L%d", min(rs[k].idx, rs[k-1].idx)+1, max(rs[k].idx, rs[k-1].idx)+1, rs[k].s)
		}
	}
	return rs, nil
}

// streamEdit copies src to w replacing each range; returns the EOL used
// for the first range. A range whose new text equals its lines is a no-op
// (invalid_args, the write is aborted).
func streamEdit(src io.Reader, w io.Writer, rs []lineRange) (string, error) {
	br := bufio.NewReaderSize(src, 1<<20)
	line, firstEOL := 1, ""
	for i := range rs {
		r := &rs[i]
		if err := copyLines(br, w, r.s-line); err != nil {
			return "", err
		}
		eol, err := r.replace(br, w)
		if err != nil {
			return "", err
		}
		if i == 0 {
			firstEOL = eol
		}
		line = r.e + 1
	}
	if _, err := io.Copy(w, br); err != nil {
		return "", fmt.Errorf("codetools: copy: %w", err)
	}
	return firstEOL, nil
}

// replace consumes lines r.s..r.e from br and writes the new text with the
// first replaced line's EOL; it returns that EOL.
func (r *lineRange) replace(br *bufio.Reader, w io.Writer) (string, error) {
	var oldText strings.Builder
	eol, lastEOL := "", ""
	for n := r.s; n <= r.e; n++ {
		l, err := readFullLine(br, largeDiffCap-r.oldBytes)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		content, le := splitEOL(l.data)
		if n == r.s {
			eol = le
		}
		lastEOL = le
		if r.oldBytes += l.size; r.oldBytes <= largeDiffCap {
			oldText.Write(content)
			oldText.WriteString(strings.Repeat("\n", len(le)/max(1, len(le))))
		}
	}
	if eol == "" {
		eol = "\n"
	}
	text := r.text
	if text != "" {
		text = strings.TrimSuffix(text, "\n") + strings.Repeat("\n", min(1, len(lastEOL)))
	}
	if r.oldBytes <= largeDiffCap && text == oldText.String() {
		return "", toolErr(CodeInvalidArgs, "", "/edits/%d: new_text equals lines %d..%d (no-op)", r.idx, r.s, r.e)
	}
	if _, err := io.WriteString(w, strings.ReplaceAll(text, "\n", eol)); err != nil {
		return "", fmt.Errorf("codetools: write: %w", err)
	}
	return eol, nil
}

// copyLines copies n lines from br to w without holding a line in memory.
func copyLines(br *bufio.Reader, w io.Writer, n int) error {
	for n > 0 {
		chunk, err := br.ReadSlice('\n')
		if _, werr := w.Write(chunk); werr != nil {
			return fmt.Errorf("codetools: write: %w", werr)
		}
		switch {
		case err == nil:
			n--
		case errors.Is(err, bufio.ErrBufferFull):
		default:
			return fmt.Errorf("codetools: read: %w", err)
		}
	}
	return nil
}

type fullLine struct {
	data []byte // at most keep bytes plus the EOL
	size int    // real byte length
}

// readFullLine consumes one line, keeping at most keep bytes of it (and its EOL).
func readFullLine(br *bufio.Reader, keep int) (fullLine, error) {
	var l fullLine
	for {
		chunk, err := br.ReadSlice('\n')
		l.size += len(chunk)
		if room := keep - len(l.data); room > 0 {
			l.data = append(l.data, chunk[:min(room, len(chunk))]...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			if bytes.HasSuffix(chunk, []byte{'\n'}) && !bytes.HasSuffix(l.data, []byte{'\n'}) {
				l.data = append(l.data, '\n')
			}
			if err != nil {
				return l, fmt.Errorf("codetools: read line: %w", err)
			}
			return l, nil
		}
	}
}

func splitEOL(l []byte) ([]byte, string) {
	switch {
	case bytes.HasSuffix(l, []byte("\r\n")):
		return l[:len(l)-2], "\r\n"
	case bytes.HasSuffix(l, []byte("\n")):
		return l[:len(l)-1], "\n"
	}
	return l, ""
}
