package netx

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/redact"
)

// DLP refusals (GAP-SEC-16); all are class bad_request, nothing is sent.
var (
	// ErrSecretEgress means a tool request carries a §14.3 secret pattern
	// or a known secret value in some encoding.
	ErrSecretEgress = errors.New("blocked_host: secret_egress")
	// ErrDLPLimit refuses a request the gate cannot scan within its bounds:
	// a body over dlpMaxBody or a scan over dlpMaxVerify (fail-closed).
	ErrDLPLimit = errors.New("blocked_host: dlp_limit")
)

// DLP bounds (ADR-100 butir 8, amended after REVIEW-F1-SEC1).
const (
	dlpWindow    = 8       // bytes of the indexed form prefix
	dlpMaxDepth  = 3       // percent/form-decoding layers after the raw one
	dlpMaxBody   = 8 << 20 // larger tool request bodies are refused
	dlpMaxVerify = 1 << 18 // form bytes compared per request before fail-closed
	dlpFilterLog = 20      // 2^20-bit prefilter
	// dlpPatternReach: bytes around a decoded byte rescanned for §14.3
	// patterns (every pattern's shortest match is < 256 bytes).
	dlpPatternReach = 256
	minEncoded      = 8 // shortest encoded (non-raw) form matched
)

// dlp is the single egress DLP gate for Tool requests. Known secrets are
// matched case-insensitively (ASCII) on every decoding layer through an
// 8-byte-prefix index behind a bitset prefilter; the §14.3 patterns run
// through redact. Immutable after build; swapped atomically on SetSecrets.
type dlp struct {
	pat    *redact.Redactor // built-in §14.3 patterns only
	log    *redact.Redactor // patterns + raw secrets, for log fields
	filter []uint64
	grams  []uint64            // 2^20-bit prefilter of the 4-grams at form offsets 0..3
	index  map[uint64][][]byte // folded 8-byte prefix → folded forms
	// Folded raw secrets shorter than dlpWindow: 1-byte ones as a set,
	// longer ones indexed by their first two bytes.
	short1   [4]uint64
	short2   []uint64 // 2^16-bit prefilter
	shortIdx map[uint16][][]byte
	region   int // bytes scanned around each decoded byte (≥ longest form)
	// hasShort1/hasShort2: some 1-byte / 2..7-byte raw secret exists.
	hasShort1, hasShort2 bool
	shortStride          int       // probe stride = shortest 2..7-byte secret − 1
	shortGrams           []uint64  // 2^16-bit set of 2-grams at offsets < shortStride
	scratch              sync.Pool // *[]byte folding buffers
	layers               sync.Pool // *dlpLayers decoding buffers
	// keepCRLF: some secret form contains CR or LF, so folding keeps them.
	keepCRLF bool
	lower    [256]byte // ASCII lowercase table
}

func newDLP(secrets []string) *dlp {
	d := &dlp{
		pat:    redact.New(redact.Options{}),
		log:    redact.New(redact.Options{Secrets: secrets}),
		filter: make([]uint64, 1<<dlpFilterLog/64),
		grams:  make([]uint64, 1<<dlpFilterLog/64),
		index:  map[uint64][][]byte{},
		short2: make([]uint64, 1<<16/64), shortIdx: map[uint16][][]byte{},
		shortGrams: make([]uint64, 1<<16/64),
		region:     dlpPatternReach, shortStride: dlpWindow - 1,
	}
	d.initLower()
	for _, form := range secretForms(secrets) {
		f := foldAll([]byte(form))
		d.keepCRLF = d.keepCRLF || bytes.ContainsAny(f, "\r\n")
		d.region = max(d.region, len(f))
		switch {
		case len(f) == 1:
			d.short1[f[0]/64] |= 1 << (f[0] % 64)
			d.hasShort1 = true
			continue
		case len(f) < dlpWindow:
			d.hasShort2 = true
			d.shortStride = min(d.shortStride, len(f)-1)
			k2 := uint16(f[0])<<8 | uint16(f[1])
			d.short2[k2/64] |= 1 << (k2 % 64)
			if !slices.ContainsFunc(d.shortIdx[k2], func(g []byte) bool { return bytes.Equal(g, f) }) {
				d.shortIdx[k2] = append(d.shortIdx[k2], f)
			}
			continue
		}
		k := key(f)
		d.filter[mix(k)/64] |= 1 << (mix(k) % 64)
		for off := range 4 { // the 4-grams a stride-4 probe can land on
			g := gram(f[off:])
			d.grams[mix32(g)/64] |= 1 << (mix32(g) % 64)
		}
		if !slices.ContainsFunc(d.index[k], func(g []byte) bool { return bytes.Equal(g, f) }) {
			d.index[k] = append(d.index[k], f)
		}
	}
	for _, forms := range d.shortIdx {
		for _, f := range forms {
			for off := range d.shortStride { // 2-grams a stride probe can land on
				g := uint16(f[off])<<8 | uint16(f[off+1])
				d.shortGrams[g/64] |= 1 << (g % 64)
			}
		}
	}
	return d
}

// secretForms returns, per secret, the raw value and the encodings the
// gate matches besides percent/form encoding (handled by decoding layers):
// hex, JSON string escaping, and base64 (std and URL alphabets) at all
// three byte alignments, so a secret inside a larger encoded blob is found.
func secretForms(secrets []string) []string {
	var out []string
	add := func(s string) {
		if len(s) >= minEncoded {
			out = append(out, s)
		}
	}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		out = append(out, s) // raw: any length (redact fail-closed rule)
		add(hex.EncodeToString([]byte(s)))
		add(url.QueryEscape(s))
		add(url.PathEscape(s))
		if j, err := json.Marshal(s); err == nil {
			add(string(j[1 : len(j)-1]))
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if enc.Encode(s) == nil {
			add(strings.TrimSuffix(strings.TrimSuffix(b.String(), "\n"), `"`)[1:])
		}
		for _, e := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			for k := range 3 {
				add(alignedBase64(e, s, k))
			}
		}
	}
	return out
}

// alignedBase64 encodes s after k filler bytes and keeps only the 4-char
// groups made purely of bytes of s: the substring any encoding of a blob
// containing s at offset ≡ k (mod 3) must contain.
func alignedBase64(enc *base64.Encoding, s string, k int) string {
	e := enc.EncodeToString(append(make([]byte, k), s...))
	first := (k + 2) / 3     // first group starting at or after byte k
	last := (k + len(s)) / 3 // groups fully inside [k, k+len(s))
	if last <= first {
		return ""
	}
	return e[4*first : 4*last]
}

// foldAll is the ASCII lowercase of b (secret forms at build time).
func foldAll(b []byte) []byte {
	d := dlp{keepCRLF: true}
	d.initLower()
	return d.fold(nil, b)
}

func (d *dlp) initLower() {
	for i := range d.lower {
		c := byte(i)
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		d.lower[i] = c
	}
}

func key(b []byte) uint64 {
	var k uint64
	for _, c := range b[:dlpWindow] {
		k = k<<8 | uint64(c)
	}
	return k
}

// mix maps a key to a prefilter bit (multiplicative hash).
func mix(k uint64) uint64 { return (k * 0x9E3779B97F4A7C15) >> (64 - dlpFilterLog) }

// check scans everything req puts on the wire: URL (userinfo, host, path,
// query, fragment), Host, headers, trailers, and the body. The body is read
// once (≤ dlpMaxBody) and re-installed from the scanned bytes, so what is
// sent is what was scanned.
func (d *dlp) check(req *http.Request) error {
	var buf bytes.Buffer
	buf.WriteString(req.URL.String())
	buf.WriteByte('\n')
	buf.WriteString(req.Host)
	buf.WriteByte('\n')
	for _, h := range []http.Header{req.Header, req.Trailer} {
		for name, vals := range h {
			for _, v := range vals {
				buf.WriteString(name + ": " + v + "\n")
			}
		}
	}
	if req.Body != nil && req.Body != http.NoBody {
		body, err := io.ReadAll(io.LimitReader(req.Body, dlpMaxBody+1))
		if cerr := req.Body.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("dlp body: %w", err)
		}
		if len(body) > dlpMaxBody {
			return &classedError{err: ErrDLPLimit, class: retry.BadRequest}
		}
		req.Body, req.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		buf.Write(body)
	}
	return d.scan(buf.Bytes())
}

// scan matches b, then up to dlpMaxDepth percent/form-decoded layers
// (hex case-insensitive, '+' = space). Decoding never grows a layer, so the
// work is ≤ (1+dlpMaxDepth)·len(b). A decoded layer is scanned only around
// bytes the decoding changed (± d.region): a match touching no decoded byte
// is a contiguous copy of the previous layer and was already checked there
// (NETX-15/16).
func (d *dlp) scan(b []byte) error {
	verify := dlpMaxVerify
	if len(b) == 0 {
		return nil
	}
	if err := d.hit(b, &verify); err != nil {
		return err
	}
	bufs, ok := d.layers.Get().(*dlpLayers)
	if !ok {
		bufs = new(dlpLayers)
	}
	defer d.layers.Put(bufs)
	cur, next := b, bufs.a[:0]
	cand, all := bufs.candA[:0], true // escape candidates of cur (all = unknown)
	for range dlpMaxDepth {
		if all && bytes.IndexByte(cur, '%') < 0 && bytes.IndexByte(cur, '+') < 0 || !all && len(cand) == 0 {
			return nil
		}
		var nextCand []int
		next, nextCand, bufs.spans = d.decode(next[:0], bufs.candB[:0], bufs.spans[:0], cur, cand, all)
		bufs.candA, bufs.candB, cand, all = nextCand, cand, nextCand, false
		if len(bufs.spans) == 0 {
			return nil
		}
		for _, sp := range bufs.spans {
			if err := d.hit(next[sp[0]:sp[1]], &verify); err != nil {
				return err
			}
		}
		// Swap: the layer just checked becomes the source of the next one.
		if &cur[0] == &b[0] {
			cur, next = next, bufs.b[:0]
		} else {
			cur, next = next, cur[:0]
		}
		bufs.a, bufs.b = cur, next
	}
	return nil
}

// dlpLayers are reusable decoding buffers of one scan.
type dlpLayers struct {
	a, b         []byte
	candA, candB []int // positions of '%'/'+' written by the last decode
	spans        [][2]int
}

// hit checks one segment against the §14.3 patterns and the secret forms.
func (d *dlp) hit(seg []byte, verify *int) error {
	// Redact returns seg itself when nothing matches.
	if out := d.pat.Redact(seg); len(out) != len(seg) || len(seg) > 0 && &out[0] != &seg[0] {
		return &classedError{err: ErrSecretEgress, class: retry.BadRequest}
	}
	buf, ok := d.scratch.Get().(*[]byte)
	if !ok {
		buf = new([]byte)
	}
	*buf = d.fold((*buf)[:0], seg)
	found, err := d.match(*buf, verify)
	d.scratch.Put(buf)
	switch {
	case err != nil:
		return err
	case found:
		return &classedError{err: ErrSecretEgress, class: retry.BadRequest}
	}
	return nil
}

// decode percent/form-decodes src into dst. With all set every byte of
// src is examined; otherwise only the positions in cand (the '%'/'+' bytes
// the previous decode emitted) can be escapes and the runs between them are
// copied in bulk. It returns the positions of '%'/'+' written to dst and the
// merged spans of dst within d.region of a decoded byte.
func (d *dlp) decode(dst []byte, outCand []int, spans [][2]int, src []byte, cand []int, all bool) ([]byte, []int, [][2]int) {
	if all {
		return d.decodeAll(dst, outCand, spans, src)
	}
	from := 0
	tr := spanTrack{r: d.region, hi: -1, spans: spans}
	for _, i := range cand {
		if i < from {
			continue // consumed by the previous escape
		}
		dst = append(dst, src[from:i]...)
		c, n := decodeAt(src, i)
		if n > 0 {
			tr.add(len(dst))
		} else {
			n = 1
		}
		if c == '%' || c == '+' {
			outCand = append(outCand, len(dst))
		}
		dst = append(dst, c)
		from = i + n
	}
	dst = append(dst, src[from:]...)
	return dst, outCand, clampSpans(tr.done(), len(dst))
}

// decodeAll is decode over every byte of src (the raw layer).
func (d *dlp) decodeAll(dst []byte, outCand []int, spans [][2]int, src []byte) ([]byte, []int, [][2]int) {
	base := len(dst)
	dst = slices.Grow(dst, len(src))[:base+len(src)]
	out, j := dst[base:], 0
	tr := spanTrack{r: d.region, hi: -1, spans: spans}
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c != '+' && c != '%' {
			out[j] = c
			j++
			continue
		}
		if dc, n := decodeAt(src, i); n > 0 {
			tr.add(base + j)
			c = dc
			i += n - 1
		}
		if c == '%' || c == '+' {
			outCand = append(outCand, base+j)
		}
		out[j] = c
		j++
	}
	dst = dst[:base+j]
	return dst, outCand, clampSpans(tr.done(), len(dst))
}

// spanTrack merges the rescan windows (± r) of decoded output positions.
type spanTrack struct {
	lo, hi, r int // current window; hi < 0 = none
	spans     [][2]int
}

func (t *spanTrack) add(p int) {
	if t.hi >= 0 && p-t.r <= t.hi {
		t.hi = p + t.r + 1
		return
	}
	t.flush()
	t.lo, t.hi = max(p-t.r, 0), p+t.r+1
}

func (t *spanTrack) flush() {
	if t.hi >= 0 {
		t.spans = append(t.spans, [2]int{t.lo, t.hi})
	}
}

func (t *spanTrack) done() [][2]int {
	t.flush()
	return t.spans
}

func clampSpans(spans [][2]int, n int) [][2]int {
	for k := range spans {
		spans[k][1] = min(spans[k][1], n)
	}
	return spans
}

// fold appends the ASCII-lowercased src to dst, dropping CR and LF when
// no secret form contains them.
func (d *dlp) fold(dst, src []byte) []byte {
	if !d.keepCRLF && bytes.IndexByte(src, '\n') < 0 && bytes.IndexByte(src, '\r') < 0 {
		n := len(dst)
		dst = slices.Grow(dst, len(src))[:n+len(src)]
		out := dst[n:]
		for i, c := range src {
			out[i] = d.lower[c]
		}
		return dst
	}
	n := len(dst)
	dst = slices.Grow(dst, len(src))[:n+len(src)]
	out := dst[n:]
	j := 0
	for _, c := range src {
		out[j] = d.lower[c]
		if d.keepCRLF || c != '\r' && c != '\n' {
			j++
		}
	}
	return dst[:n+j]
}

// match reports whether folded contains a folded secret form; verify
// bounds the bytes compared (adversarial prefix collisions fail closed).
func (d *dlp) match(folded []byte, verify *int) (bool, error) {
	if d.hasShort1 || d.hasShort2 {
		if hit, err := d.matchShort(folded, verify); hit || err != nil {
			return hit, err
		}
	}
	// Every form is ≥ dlpWindow (8) bytes, so an occurrence starting at s
	// covers the 4-gram at the stride-4 probe p ∈ [s, s+3]: only probes are
	// filtered, and a probe hit verifies the four starts p-3..p.
	for p := 0; p+4 <= len(folded); p += 4 {
		g := gram(folded[p:])
		if d.grams[mix32(g)/64]&(1<<(mix32(g)%64)) == 0 {
			continue
		}
		for s := max(p-3, 0); s <= p && s+dlpWindow <= len(folded); s++ {
			k := key(folded[s:])
			if d.filter[mix(k)/64]&(1<<(mix(k)%64)) == 0 {
				continue
			}
			if hit, err := verifyAt(folded[s:], d.index[k], verify); hit || err != nil {
				return hit, err
			}
		}
	}
	return false, nil
}

// gram is the little-endian 4-byte value at b.
func gram(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// mix32 maps a 4-gram to a prefilter bit.
func mix32(g uint32) uint32 { return (g * 0x9E3779B1) >> (32 - dlpFilterLog) }

// matchShort matches the short raw secrets: 1-byte ones by set; 2..7-byte
// ones by 2-gram probes every shortStride bytes (an occurrence of length
// L ≥ shortStride+1 covers the 2-gram at some probe at offset < shortStride),
// verifying the starts a probe can belong to.
func (d *dlp) matchShort(folded []byte, verify *int) (bool, error) {
	if d.hasShort1 {
		for _, c := range folded {
			if d.short1[c/64]&(1<<(c%64)) != 0 {
				return true, nil
			}
		}
	}
	if !d.hasShort2 {
		return false, nil
	}
	st := d.shortStride
	for p := 0; p+2 <= len(folded); p += st {
		g := uint16(folded[p])<<8 | uint16(folded[p+1])
		if d.shortGrams[g/64]&(1<<(g%64)) == 0 {
			continue
		}
		for s := max(p-st+1, 0); s <= p && s+2 <= len(folded); s++ {
			k2 := uint16(folded[s])<<8 | uint16(folded[s+1])
			if d.short2[k2/64]&(1<<(k2%64)) == 0 {
				continue
			}
			if hit, err := verifyAt(folded[s:], d.shortIdx[k2], verify); hit || err != nil {
				return hit, err
			}
		}
	}
	return false, nil
}

// verifyAt reports whether at starts with one of forms.
func verifyAt(at []byte, forms [][]byte, verify *int) (bool, error) {
	for _, f := range forms {
		if *verify -= len(f); *verify < 0 {
			return false, &classedError{err: ErrDLPLimit, class: retry.BadRequest}
		}
		if bytes.HasPrefix(at, f) {
			return true, nil
		}
	}
	return false, nil
}

// pctDecode appends src with %XX (any hex case) decoded and '+' as space.
func pctDecode(dst, src []byte) []byte {
	for i := 0; i < len(src); i++ {
		c, n := decodeAt(src, i)
		dst = append(dst, c)
		i += max(n, 1) - 1
	}
	return dst
}

// decodeAt decodes src[i]: n = bytes consumed by an escape (0 = literal).
func decodeAt(src []byte, i int) (byte, int) {
	c := src[i]
	switch {
	case c == '+':
		return ' ', 1
	case c == '%' && i+2 < len(src):
		hi, ok1 := unhex(src[i+1])
		lo, ok2 := unhex(src[i+2])
		if ok1 && ok2 {
			return hi<<4 | lo, 3
		}
	}
	return c, 0
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
