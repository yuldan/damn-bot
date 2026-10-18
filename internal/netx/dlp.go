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
	dlpWorkSlack = 2 << 10 // decoding-layer bytes allowed beyond 2×input
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
	index  map[uint64][][]byte // folded 8-byte prefix → folded forms
	short  [][]byte            // folded raw secrets shorter than dlpWindow
	// keepCRLF: some secret form contains CR or LF, so folding keeps them.
	keepCRLF bool
	lower    [256]byte // ASCII lowercase table
}

func newDLP(secrets []string) *dlp {
	d := &dlp{
		pat:    redact.New(redact.Options{}),
		log:    redact.New(redact.Options{Secrets: secrets}),
		filter: make([]uint64, 1<<dlpFilterLog/64),
		index:  map[uint64][][]byte{},
	}
	d.initLower()
	for _, form := range secretForms(secrets) {
		f := foldAll([]byte(form))
		d.keepCRLF = d.keepCRLF || bytes.ContainsAny(f, "\r\n")
		if len(f) < dlpWindow {
			d.short = append(d.short, f)
			continue
		}
		k := key(f)
		d.filter[mix(k)/64] |= 1 << (mix(k) % 64)
		if !slices.ContainsFunc(d.index[k], func(g []byte) bool { return bytes.Equal(g, f) }) {
			d.index[k] = append(d.index[k], f)
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

// scan matches b and up to dlpMaxDepth percent/form-decoded layers of it
// (hex case-insensitive, '+' = space); folded layers drop CR/LF (wrapped
// base64) unless a secret form contains them. The bytes scanned across
// layers are capped at 2×len(b) + 2 KiB (raw + one full decoding layer +
// small multi-encoded spans); more decoding work fails closed.
func (d *dlp) scan(b []byte) error {
	verify, work := dlpMaxVerify, 2*len(b)+dlpWorkSlack
	var folded, next []byte
	for depth := 0; ; depth++ {
		if work -= len(b); work < 0 {
			return &classedError{err: ErrDLPLimit, class: retry.BadRequest}
		}
		if !bytes.Equal(d.pat.Redact(b), b) {
			return &classedError{err: ErrSecretEgress, class: retry.BadRequest}
		}
		folded = d.fold(folded[:0], b)
		hit, err := d.match(folded, &verify)
		switch {
		case err != nil:
			return err
		case hit:
			return &classedError{err: ErrSecretEgress, class: retry.BadRequest}
		case depth == dlpMaxDepth, bytes.IndexByte(b, '%') < 0 && bytes.IndexByte(b, '+') < 0:
			return nil // nothing left to decode
		}
		next = pctDecode(next[:0], b)
		if bytes.Equal(next, b) {
			return nil
		}
		b, next = next, b[:0:0]
	}
}

// fold appends the ASCII-lowercased src to dst, dropping CR and LF when
// no secret form contains them.
func (d *dlp) fold(dst, src []byte) []byte {
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
	for _, s := range d.short {
		if bytes.Contains(folded, s) {
			return true, nil
		}
	}
	if len(d.index) == 0 {
		return false, nil
	}
	var k uint64
	for i, c := range folded {
		k = k<<8 | uint64(c)
		if i < dlpWindow-1 || d.filter[mix(k)/64]&(1<<(mix(k)%64)) == 0 {
			continue
		}
		at := folded[i+1-dlpWindow:]
		for _, f := range d.index[k] {
			if *verify -= len(f); *verify < 0 {
				return false, &classedError{err: ErrDLPLimit, class: retry.BadRequest}
			}
			if bytes.HasPrefix(at, f) {
				return true, nil
			}
		}
	}
	return false, nil
}

// pctDecode appends src with %XX (any hex case) decoded and '+' as space.
func pctDecode(dst, src []byte) []byte {
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '+':
			c = ' '
		case c == '%' && i+2 < len(src):
			hi, ok1 := unhex(src[i+1])
			lo, ok2 := unhex(src[i+2])
			if ok1 && ok2 {
				c = hi<<4 | lo
				i += 2
			}
		}
		dst = append(dst, c)
	}
	return dst
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
