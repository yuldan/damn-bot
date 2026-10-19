package netx

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	minEncoded   = 8       // shortest encoded (non-raw) form matched
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
	region   int // folded bytes scanned around a decoded byte (= longest form)
	// inForm: folded bytes that occur in some secret form; a decoded byte
	// outside it (and kept by fold) cannot be part of an occurrence.
	inForm      [256]bool
	spaceInForm bool // inForm[' ']: form-encoded '+' needs a window
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
		region:     dlpWindow, shortStride: dlpWindow - 1,
	}
	d.initLower()
	for _, form := range secretForms(secrets) {
		f := foldAll([]byte(form))
		d.keepCRLF = d.keepCRLF || bytes.ContainsAny(f, "\r\n")
		d.region = max(d.region, len(f))
		for _, c := range f {
			d.inForm[c] = true
		}
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
	d.spaceInForm = d.inForm[' ']
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

// scan matches b, then up to dlpMaxDepth percent/form-decoded layers.
// Decoding never grows a layer: work ≤ (1+dlpMaxDepth)·len(b). A decoded
// layer is rechecked only where a match could be new (layerHit, NETX-15/16,
// NETX-26).
func (d *dlp) scan(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	budget := verifyBudget{long: dlpMaxVerify, short: dlpMaxVerify + dlpShortVerifyPerByte*len(b)}
	if err := d.hit(b, &budget); err != nil {
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
		tr := spanTrack{gap: 2 * d.region, last: -1, spans: bufs.spans[:0], pats: bufs.pats[:0], patLimit: len(cur) >> maxCandShift}
		var nextCand []int32
		var overflow bool
		next, nextCand, overflow = d.decode(next[:0], bufs.candB[:0], &tr, cur, cand, all)
		bufs.candA, bufs.candB, cand, all = nextCand, cand, nextCand, overflow
		bufs.spans, bufs.pats = tr.done(), tr.pats
		if !tr.decoded {
			return nil // nothing decoded: deeper layers are this one again
		}
		if err := d.layerHit(next, &tr, &budget); err != nil {
			return err
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

// layerHit checks a decoded layer: patterns in the runs around decoded
// bytes, secrets in the windows around form-relevant decoded bytes.
func (d *dlp) layerHit(layer []byte, tr *spanTrack, budget *verifyBudget) error {
	if d.patternRunsHit(layer, tr) {
		return &classedError{err: ErrSecretEgress, class: retry.BadRequest}
	}
	for _, sp := range d.widen(layer, tr.spans) {
		if err := d.secretHit(layer[sp[0]:sp[1]], budget); err != nil {
			return err
		}
	}
	return nil
}

// dlpShortVerifyPerByte: short-secret comparison bytes allowed per input
// byte on top of dlpMaxVerify (2-byte keys hit prose often; NETX-27).
const dlpShortVerifyPerByte = 8

// verifyBudget bounds the bytes compared per scan (fail-closed).
type verifyBudget struct{ long, short int }

// dlpLayers are reusable decoding buffers of one scan.
type dlpLayers struct {
	a, b         []byte
	candA, candB []int32 // positions of '%'/'+' written by the last decode
	spans        [][2]int
	pats         []int32 // decoded non-space positions (spanTrack.pats)
}

// hit checks one segment against the §14.3 patterns and the secret forms.
func (d *dlp) hit(seg []byte, budget *verifyBudget) error {
	if d.patternHit(seg) {
		return &classedError{err: ErrSecretEgress, class: retry.BadRequest}
	}
	return d.secretHit(seg, budget)
}

// patternHit reports whether seg contains a §14.3 pattern (Redact returns
// seg itself when nothing matches).
func (d *dlp) patternHit(seg []byte) bool {
	out := d.pat.Redact(seg)
	return len(out) != len(seg) || len(seg) > 0 && &out[0] != &seg[0]
}

// secretHit checks seg against the known secret forms.
func (d *dlp) secretHit(seg []byte, budget *verifyBudget) error {
	buf, ok := d.scratch.Get().(*[]byte)
	if !ok {
		buf = new([]byte)
	}
	*buf = d.fold((*buf)[:0], seg)
	found, err := d.match(*buf, budget)
	d.scratch.Put(buf)
	switch {
	case err != nil:
		return err
	case found:
		return &classedError{err: ErrSecretEgress, class: retry.BadRequest}
	}
	return nil
}

// maxCandShift: more than len>>maxCandShift '%'/'+' candidates make the
// next layer a full pass instead of a list (NETX-28).
const maxCandShift = 4

// decode percent/form-decodes src into dst: every byte (all), or only the
// '%'/'+' candidates cand left by the previous decode, copying the runs
// between them. It returns dst's candidates (overflow: too many) and
// records decoded positions in tr.
func (d *dlp) decode(dst []byte, outCand []int32, tr *spanTrack, src []byte, cand []int32, all bool) ([]byte, []int32, bool) {
	if all {
		return d.decodeAll(dst, outCand, tr, src)
	}
	from := 0
	limit := len(src) >> maxCandShift
	overflow := false
	for _, ci := range cand {
		i := int(ci)
		if i < from {
			continue // consumed by the previous escape
		}
		dst = append(dst, src[from:i]...)
		c, n := decodeAt(src, i)
		if n > 0 {
			tr.add(len(dst), c, d.secretByte(c))
		} else {
			n = 1
		}
		if c == '%' || c == '+' {
			outCand, overflow = addCand(outCand, len(dst), limit, overflow)
		}
		dst = append(dst, c)
		from = i + n
	}
	dst = append(dst, src[from:]...)
	return dst, outCand, overflow
}

// addCand records candidate p unless the list is over limit.
func addCand(cand []int32, p, limit int, overflow bool) ([]int32, bool) {
	if overflow || len(cand) >= limit || p < 0 || p > math.MaxInt32 {
		return cand[:0], true
	}
	return append(cand, int32(p)), false
}

// decodeAll is decode over every byte of src (the raw layer).
func (d *dlp) decodeAll(dst []byte, outCand []int32, tr *spanTrack, src []byte) ([]byte, []int32, bool) {
	base := len(dst)
	dst = slices.Grow(dst, len(src))[:base+len(src)]
	out, j := dst[base:], 0
	limit := len(src) >> maxCandShift
	overflow := false
	if bytes.IndexByte(src, '+') >= 0 {
		tr.decoded = true // '+' decodes to a space
	}
	for i := 0; i < len(src); {
		// The run up to the next '%' is copied in bulk with '+' → ' '.
		end := len(src)
		if k := bytes.IndexByte(src[i:], '%'); k >= 0 {
			end = i + k
		}
		if d.spaceInForm {
			plusSpans(tr, base+j, src[i:end])
		}
		j += copyFormSpaces(out[j:], src[i:end])
		if i = end; i == len(src) {
			break
		}
		c, n := decodeAt(src, i)
		switch {
		case n > 0:
			tr.add(base+j, c, d.secretByte(c))
			if c == '%' || c == '+' {
				outCand, overflow = addCand(outCand, base+j, limit, overflow)
			}
			i += n
		default: // a literal '%' stays a candidate for the next layer
			outCand, overflow = addCand(outCand, base+j, limit, overflow)
			i++
		}
		out[j] = c
		j++
	}
	return dst[:base+j], outCand, overflow
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
func (d *dlp) match(folded []byte, budget *verifyBudget) (bool, error) {
	if d.hasShort1 || d.hasShort2 {
		if hit, err := d.matchShort(folded, &budget.short); hit || err != nil {
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
			if hit, err := verifyAt(folded[s:], d.index[k], &budget.long); hit || err != nil {
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
