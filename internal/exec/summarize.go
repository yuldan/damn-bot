package exec

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/redact"
)

// Summary limits (§A.3, P5).
const (
	InlineChars     = 4000   // larger output spills to a blob
	TurnInlineChars = 24_000 // inline characters per turn before short previews
	previewHead     = 160
	previewErrors   = 240
	previewTail     = 400
	shortHead       = 80
	shortErrors     = 120
	shortTail       = 200
	errorLines      = 3
	errorLineChars  = 80
)

// Summary is the model-facing form of one Result (§A.3).
type Summary struct {
	Header string // "[exit 1 · 2.341 ms · signal none · runner pipe · status exited]"
	Text   string // inline output, or the preview when Spilled
	// Spilled: Log (steps 1–5) and Raw (raw output, redacted) must be
	// stored as blobs; Text already names the blob path from spill.
	Spilled        bool
	Log, Raw       []byte
	CharsModel     int // characters of Text
	LinesCollapsed int // lines removed by CR/progress/dedupe collapsing
}

// Summarizer runs the deterministic pipeline of §A.3 (ADR-105 butir 19).
type Summarizer struct {
	red     *redact.Redactor
	errLine *regexp.Regexp
}

// NewSummarizer returns a pipeline redacting with red (patterns plus the
// session's known secrets, SPEC §14.3).
func NewSummarizer(red *redact.Redactor) *Summarizer {
	return &Summarizer{
		red:     red,
		errLine: regexp.MustCompile(`(?i)\b(error|fatal|panic|exception|traceback|failed|fail:|denied|not found)\b|^\S+:\d+(:\d+)?: `),
	}
}

// Summarize renders res. inlineLeft is what remains of the turn's inline
// budget (TurnInlineChars minus earlier results); spill stores the blobs
// and returns the path shown in the preview.
func (z *Summarizer) Summarize(res *Result, inlineLeft int, spill func(log, raw []byte) (string, error)) (Summary, error) {
	s := Summary{Header: header(res)}
	lines, collapsed := cleanLines(res.Output)
	s.LinesCollapsed = collapsed
	text := z.red.Redact([]byte(strings.Join(lines, "\n")))
	n := utf8.RuneCount(text)
	if n <= InlineChars && n <= inlineLeft {
		s.Text, s.CharsModel = string(text), n
		return s, nil
	}
	raw := z.red.Redact(res.Output)
	if stripped := z.red.Redact(stripANSI(nil, res.Output)); !bytes.Equal(stripped, stripANSI(nil, raw)) {
		// SGR codes split a secret in the raw bytes; the ANSI-free,
		// re-redacted form is the only safe blob (EX47-2).
		raw = stripped
	}
	s.Spilled, s.Log, s.Raw = true, text, raw
	path, err := spill(s.Log, s.Raw)
	if err != nil {
		return Summary{}, fmt.Errorf("exec: spill output: %w", err)
	}
	head, errs, tail := previewHead, previewErrors, previewTail
	if inlineLeft < InlineChars {
		head, errs, tail = shortHead, shortErrors, shortTail
	}
	s.Text = z.preview(string(text), head, errs, tail) + "\n[full output: " + path + "]"
	s.CharsModel = utf8.RuneCountInString(s.Text)
	return s, nil
}

// header is the uncompressed first line of every shell result.
func header(res *Result) string {
	code := "none"
	if res.ExitCode != nil {
		code = strconv.Itoa(*res.ExitCode)
	}
	sig := res.Signal
	if sig == "" {
		sig = "none"
	}
	return fmt.Sprintf("[exit %s · %s ms · signal %s · runner %s · status %s]",
		code, groupThousands(res.Duration.Milliseconds()), sig, res.Runner, res.Status)
}

// groupThousands writes n with '.' thousands separators (2.341).
func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// cleanLines is steps 1–4: valid UTF-8, escapes stripped, CR and
// backspace applied, progress runs and repeats collapsed to their last
// line plus " [×N]".
func cleanLines(out []byte) ([]string, int) {
	clean := stripANSI(make([]byte, 0, len(out)), out)
	var lines []string
	for raw := range bytes.SplitSeq(clean, []byte{'\n'}) {
		lines = append(lines, applyCR(raw))
	}
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // final newline
	}
	collapsed := 0
	lines, collapsed = collapseRuns(lines, progressKey, collapsed)
	lines, collapsed = collapseRuns(lines, identityKey, collapsed)
	return lines, collapsed
}

// applyCR keeps what a terminal shows of one line: the segment after the
// last carriage return (a CRLF ending is not a redraw), backspaces applied,
// invalid UTF-8 bytes as U+FFFD.
func applyCR(line []byte) string {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if i := bytes.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	if bytes.IndexByte(line, '\b') < 0 && !hasCtl(line) && utf8.Valid(line) {
		return string(line) // common case: nothing to rewrite
	}
	out := make([]rune, 0, len(line))
	for len(line) > 0 {
		r, size := utf8.DecodeRune(line)
		line = line[size:]
		switch {
		case r == '\b':
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		case stripCtl(r):
			// C0 (except tab), DEL and C1 are not text (EX47-1).
		default:
			out = append(out, r) // RuneError for an invalid byte = U+FFFD
		}
	}
	return string(out)
}

// stripCtl reports whether r is a C0 control other than tab, DEL, or a C1
// control: the model context keeps only printable text.
func stripCtl(r rune) bool {
	return (r < 0x20 && r != '\t') || (r >= 0x7f && r <= 0x9f)
}

// hasCtl is the fast-path check for [stripCtl]; C1 controls appear as
// U+0080..U+009F (0xC2 0x80..0x9F in UTF-8).
func hasCtl(b []byte) bool {
	for _, c := range b {
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return true
		}
	}
	for i := 0; i+1 < len(b); i++ {
		if b[i] == 0xc2 && b[i+1] >= 0x80 && b[i+1] <= 0x9f {
			return true
		}
	}
	return false
}

// progressKey appends the progress-collapsing key of s to dst: digits →
// '#', whitespace runs → one space.
func progressKey(dst []byte, s string) []byte {
	space := false
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			dst = append(dst, '#')
			space = false
		case c == ' ' || c == '\t':
			if !space {
				dst = append(dst, ' ')
			}
			space = true
		default:
			dst = append(dst, c)
			space = false
		}
	}
	return dst
}

func identityKey(dst []byte, s string) []byte { return append(dst, s...) }

// collapseRuns replaces each run of consecutive lines with the same key by
// its last line, suffixed " [×N]" (an existing suffix is merged). Keys are
// built in two reused buffers.
func collapseRuns(lines []string, key func([]byte, string) []byte, collapsed int) ([]string, int) {
	out := lines[:0:0]
	var k, kj []byte
	for i := 0; i < len(lines); {
		k = key(k[:0], lines[i])
		j, count := i+1, repeatCount(lines[i])
		for j < len(lines) {
			if kj = key(kj[:0], lines[j]); !bytes.Equal(kj, k) {
				break
			}
			count += repeatCount(lines[j])
			j++
		}
		last := lines[i]
		if j-i > 1 {
			last = trimRepeat(lines[j-1]) + " [×" + strconv.Itoa(count) + "]"
			collapsed += j - i - 1
		}
		out = append(out, last)
		i = j
	}
	return out, collapsed
}

func repeatCount(s string) int {
	i := strings.LastIndex(s, " [×")
	if i < 0 || !strings.HasSuffix(s, "]") {
		return 1
	}
	n, err := strconv.Atoi(s[i+len(" [×") : len(s)-1])
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func trimRepeat(s string) string {
	if repeatCount(s) > 1 {
		return s[:strings.LastIndex(s, " [×")]
	}
	return s
}

// preview is head + up to errorLines unique error lines from the middle +
// tail, in runes; unused head/error budget goes to the tail.
func (z *Summarizer) preview(text string, head, errs, tail int) string {
	r := []rune(text)
	h := min(head, len(r))
	headPart := string(r[:h])
	rest := r[h:]
	var found []string
	used := 0
	mid := string(rest[:max(len(rest)-tail, 0)])
	for line := range strings.SplitSeq(mid, "\n") {
		if len(found) == errorLines || !z.errLine.MatchString(line) {
			continue
		}
		cut := string([]rune(line)[:min(errorLineChars, utf8.RuneCountInString(line))])
		if used+utf8.RuneCountInString(cut) > errs || slices.Contains(found, cut) {
			continue
		}
		found = append(found, cut)
		used += utf8.RuneCountInString(cut)
	}
	tailN := min(tail+(head-h)+(errs-used), len(rest))
	parts := []string{headPart}
	if len(found) > 0 {
		parts = append(parts, "…", strings.Join(found, "\n"))
	}
	if len(rest) > tailN {
		parts = append(parts, "…")
	}
	parts = append(parts, string(rest[len(rest)-tailN:]))
	return strings.Join(parts, "\n")
}
