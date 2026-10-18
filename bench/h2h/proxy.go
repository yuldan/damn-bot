// Package h2h adalah harness head-to-head berbasis LLM (ROADMAP F0 #2, SCORECARD §2.3):
// proxy rekam lokal di depan provider OpenAI-compatible, server snapshot/fault lokal,
// grader eval set v1, dan runner per agent. Token/biaya dihitung dari data proxy, bukan
// dari laporan agent.
package h2h

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Redacted menggantikan nilai header rahasia dan kemunculan kunci upstream di rekaman.
const Redacted = "REDACTED"

// sensitiveHeaders tidak pernah ditulis apa adanya ke rekaman (kanonik http.Header).
var sensitiveHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"X-Api-Key":           true,
	"Api-Key":             true,
	"Cookie":              true,
	"Set-Cookie":          true,
}

// hopHeaders adalah header hop-by-hop (RFC 9110 §7.6.1) yang tidak diteruskan.
var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// Price adalah tarif USD per 1 juta token.
type Price struct {
	Input       float64 `json:"input_per_mtok"`
	CachedInput float64 `json:"cached_input_per_mtok"`
	Output      float64 `json:"output_per_mtok"`
}

// Usage adalah usage satu respons (atau jumlahnya), semantik OpenAI chat completions:
// PromptTokens sudah termasuk CachedTokens.
type Usage struct {
	Calls            int     `json:"calls"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// Add menjumlahkan u2 ke u.
func (u *Usage) Add(u2 Usage) {
	u.Calls += u2.Calls
	u.PromptTokens += u2.PromptTokens
	u.CompletionTokens += u2.CompletionTokens
	u.CachedTokens += u2.CachedTokens
	u.ReasoningTokens += u2.ReasoningTokens
	u.CostUSD += u2.CostUSD
}

// Cost menghitung USD dari tabel harga: input tak ter-cache, input ter-cache, output.
func (p Price) Cost(u Usage) float64 {
	uncached := max(u.PromptTokens-u.CachedTokens, 0)
	return (float64(uncached)*p.Input + float64(u.CachedTokens)*p.CachedInput +
		float64(u.CompletionTokens)*p.Output) / 1e6
}

// Record adalah satu pertukaran request/response; satu objek JSON per baris.
type Record struct {
	Seq          int         `json:"seq"`
	Agent        string      `json:"agent"`
	Task         string      `json:"task"`
	Start        time.Time   `json:"start"`
	DurationMS   float64     `json:"duration_ms"`
	Method       string      `json:"method"`
	Path         string      `json:"path"`
	ReqHeader    http.Header `json:"req_header"`
	ReqBody      any         `json:"req_body"` // JSON apa adanya bila valid, selain itu string
	Status       int         `json:"status"`
	RespHeader   http.Header `json:"resp_header,omitempty"`
	Stream       bool        `json:"stream"`
	RespBody     string      `json:"resp_body"` // byte respons persis (SSE utuh), sesudah dekompresi transport
	Usage        *Usage      `json:"usage,omitempty"`
	UsageMissing bool        `json:"usage_missing,omitempty"` // completion 200 tanpa objek usage
	Error        string      `json:"error,omitempty"`
}

// Recorder adalah reverse proxy rekam. Klien (agent) memakai kunci dummy; kunci upstream
// asli hanya disuntik di request keluar dan tidak pernah masuk rekaman.
type Recorder struct {
	Upstream *url.URL // mis. https://api.example.com (path request klien dipertahankan)
	Key      string
	Price    Price
	CapUSD   float64 // 0 = tanpa batas; bila tercapai, request completion ditolak 402 tanpa diteruskan
	Client   *http.Client
	// Background menerima rekaman request di luar task aktif (mis. daemon agent yang masih
	// hidup); nil = hanya dihitung biayanya.
	Background io.Writer

	mu     sync.Mutex
	w      io.Writer
	agent  string
	task   string
	seq    int
	spent  float64
	usage  Usage
	errs   []string
	bg     Usage
	bgErrs []string
}

// Begin mengarahkan rekaman berikutnya ke w dan me-reset akumulator per task.
func (r *Recorder) Begin(agent, task string, w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agent, r.task, r.w = agent, task, w
	r.usage, r.errs = Usage{}, nil
}

// End menutup task aktif dan mengembalikan usage serta error harness (proxy) task itu.
func (r *Recorder) End() (Usage, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u, e := r.usage, r.errs
	r.w, r.task, r.agent = nil, "", ""
	return u, e
}

// Spent adalah total USD sejak proxy dibuat.
func (r *Recorder) Spent() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spent
}

func (r *Recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rec := Record{Start: time.Now().UTC(), Method: req.Method, Path: req.URL.RequestURI(), ReqHeader: redactHeader(req.Header)}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		r.fail(w, &rec, http.StatusBadRequest, "read request: "+err.Error())
		return
	}
	rec.ReqBody = jsonOrString(body)
	var hdr struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &hdr) // body non-JSON (GET) → Stream=false
	rec.Stream = hdr.Stream
	billable := strings.HasSuffix(req.URL.Path, "/chat/completions")

	if billable && r.CapUSD > 0 && r.Spent() >= r.CapUSD {
		r.fail(w, &rec, http.StatusPaymentRequired, fmt.Sprintf("budget cap %.2f USD tercapai", r.CapUSD))
		return
	}

	out, err := http.NewRequestWithContext(req.Context(), req.Method, r.Upstream.JoinPath(req.URL.Path).String(), bytes.NewReader(body))
	if err != nil {
		r.fail(w, &rec, http.StatusBadGateway, err.Error())
		return
	}
	out.URL.RawQuery = req.URL.RawQuery
	for k, v := range req.Header {
		out.Header[k] = v
	}
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	// Transport Go menegosiasikan gzip sendiri dan mendekompresi, sehingga rekaman = teks polos.
	out.Header.Del("Accept-Encoding")
	out.Header.Del("X-Api-Key")
	out.Header.Set("Authorization", "Bearer "+r.Key)

	resp, err := r.Client.Do(out)
	if err != nil {
		r.fail(w, &rec, http.StatusBadGateway, "upstream: "+err.Error())
		return
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	for _, h := range append(hopHeaders, "Content-Length", "Content-Encoding") {
		w.Header().Del(h)
	}
	w.WriteHeader(resp.StatusCode)
	rec.Status, rec.RespHeader = resp.StatusCode, redactHeader(resp.Header)

	// Teruskan per potongan dan flush agar SSE tetap streaming; salin seluruhnya ke rekaman.
	var buf bytes.Buffer
	fl, _ := w.(http.Flusher)
	chunk := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if _, werr := w.Write(chunk[:n]); werr != nil && rec.Error == "" {
				rec.Error = "client write: " + werr.Error()
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				rec.Error = "upstream read: " + rerr.Error()
			}
			break
		}
	}
	rec.RespBody = buf.String()

	if billable && resp.StatusCode == http.StatusOK {
		if u, ok := ParseUsage(buf.Bytes(), rec.Stream); ok {
			u.CostUSD = r.Price.Cost(u)
			rec.Usage = &u
		} else {
			rec.UsageMissing = true
		}
	}
	r.write(&rec)
}

// fail menjawab klien dengan error JSON bergaya OpenAI dan merekamnya sebagai error harness.
func (r *Recorder) fail(w http.ResponseWriter, rec *Record, status int, msg string) {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": "h2h proxy: " + msg, "type": "h2h_proxy"}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
	rec.Status, rec.RespBody, rec.Error = status, string(b), msg
	r.write(rec)
}

func (r *Recorder) write(rec *Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	rec.Seq, rec.Agent, rec.Task = r.seq, r.agent, r.task
	rec.DurationMS = float64(time.Since(rec.Start).Microseconds()) / 1e3
	usage, errs, w := &r.usage, &r.errs, r.w
	if w == nil {
		// Di luar task: dicatat terpisah agar tidak hilang saat Begin me-reset akumulator.
		usage, errs, w = &r.bg, &r.bgErrs, r.Background
	}
	if rec.Usage != nil {
		usage.Add(*rec.Usage)
		r.spent += rec.Usage.CostUSD
	}
	if rec.Error != "" {
		*errs = append(*errs, fmt.Sprintf("seq %d %s: %s", rec.Seq, rec.Path, rec.Error))
	}
	if rec.UsageMissing {
		*errs = append(*errs, fmt.Sprintf("seq %d %s: usage tidak ada di respons", rec.Seq, rec.Path))
	}
	if w == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		*errs = append(*errs, "marshal record: "+err.Error())
		return
	}
	if r.Key != "" {
		// Lapisan kedua: kunci upstream tidak boleh muncul walau upstream menggemakannya.
		line = bytes.ReplaceAll(line, []byte(r.Key), []byte(Redacted))
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		*errs = append(*errs, "write record: "+err.Error())
	}
}

// BackgroundStats mengembalikan usage dan error request di luar task.
func (r *Recorder) BackgroundStats() (Usage, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bg, append([]string(nil), r.bgErrs...)
}

func redactHeader(h http.Header) http.Header {
	out := h.Clone()
	for k := range out {
		if sensitiveHeaders[http.CanonicalHeaderKey(k)] {
			out[k] = []string{Redacted}
		}
	}
	return out
}

func jsonOrString(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}

type wireUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// ParseUsage mengambil usage dari body JSON atau, bila stream, dari chunk SSE terakhir
// yang membawa usage (stream_options.include_usage).
func ParseUsage(body []byte, stream bool) (Usage, bool) {
	var found *wireUsage
	parse := func(b []byte) {
		var m struct {
			Usage *wireUsage `json:"usage"`
		}
		if json.Unmarshal(b, &m) == nil && m.Usage != nil {
			found = m.Usage
		}
	}
	if stream {
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data:")
			if !ok {
				continue
			}
			if data = strings.TrimSpace(data); data != "[DONE]" {
				parse([]byte(data))
			}
		}
	} else {
		parse(body)
	}
	if found == nil {
		return Usage{}, false
	}
	return Usage{
		Calls:            1,
		PromptTokens:     found.PromptTokens,
		CompletionTokens: found.CompletionTokens,
		CachedTokens:     found.PromptTokensDetails.CachedTokens,
		ReasoningTokens:  found.CompletionTokensDetails.ReasoningTokens,
	}, true
}
