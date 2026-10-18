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
	AbortedCalls     int     `json:"aborted_calls"` // completion yang diputus agent; usage tak diketahui
}

// Add menjumlahkan u2 ke u.
func (u *Usage) Add(u2 Usage) {
	u.Calls += u2.Calls
	u.PromptTokens += u2.PromptTokens
	u.CompletionTokens += u2.CompletionTokens
	u.CachedTokens += u2.CachedTokens
	u.ReasoningTokens += u2.ReasoningTokens
	u.CostUSD += u2.CostUSD
	u.AbortedCalls += u2.AbortedCalls
}

// Cost menghitung USD dari tabel harga: input tak ter-cache, input ter-cache, output.
func (p Price) Cost(u Usage) float64 {
	uncached := max(u.PromptTokens-u.CachedTokens, 0)
	return (float64(uncached)*p.Input + float64(u.CachedTokens)*p.CachedInput +
		float64(u.CompletionTokens)*p.Output) / 1e6
}

// Valid: tarif input dan output > 0, cached ≥ 0 (provider tanpa diskon cache, ADR-085). Tarif
// input/output 0 membuat biaya = 0 sehingga cap tidak pernah terpicu (AUD-037).
func (p Price) Valid() bool { return p.Input > 0 && p.CachedInput >= 0 && p.Output > 0 }

// DefaultMaxOutput adalah proyeksi token output bila request tidak membatasi max token.
const DefaultMaxOutput = 32768

// Worst memproyeksikan biaya terburuk satu request sebelum diteruskan (reservasi, ADR-075):
// input = ceil(byte body / 2) token, selalu ≥ token nyata (o200k rata-rata > 2 byte/token pada
// JSON), tanpa diskon cache; output = max_tokens/max_completion_tokens/max_output_tokens atau
// DefaultMaxOutput.
func (p Price) Worst(body []byte) float64 {
	var m struct {
		MaxTokens           int `json:"max_tokens"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
		MaxOutputTokens     int `json:"max_output_tokens"`
	}
	_ = json.Unmarshal(body, &m) // body bukan JSON → default
	out := max(m.MaxTokens, m.MaxCompletionTokens, m.MaxOutputTokens)
	if out <= 0 {
		out = DefaultMaxOutput
	}
	in := (len(body) + 1) / 2
	return (float64(in)*p.Input + float64(out)*p.Output) / 1e6
}

// billablePaths: endpoint POST yang biayanya dihitung. POST lain ditolak, karena biayanya tidak
// bisa dihitung proxy (AUD-037 butir 5).
var billablePaths = []string{"/chat/completions", "/responses"}

func billablePath(p string) bool {
	for _, s := range billablePaths {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	return false
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
	// ClientAborted: agent memutus request-nya sendiri sebelum selesai (bukan error harness);
	// usage tidak diketahui, upstream mungkin tetap menagih sebagian.
	ClientAborted bool   `json:"client_aborted,omitempty"`
	Note          string `json:"note,omitempty"`
	Error         string `json:"error,omitempty"`
	// ReservedUSD: proyeksi terburuk yang direservasi sebelum forward; ChargedUSD: yang dicatat ke
	// biaya (usage nyata, atau ReservedUSD bila usage tidak diketahui: usage_missing, abort, error).
	ReservedUSD float64 `json:"reserved_usd,omitempty"`
	ChargedUSD  float64 `json:"charged_usd,omitempty"`
	WorstCase   bool    `json:"worst_case_charged,omitempty"`
}

// Recorder adalah reverse proxy rekam. Klien (agent) memakai kunci dummy; kunci upstream
// asli hanya disuntik di request keluar dan tidak pernah masuk rekaman.
type Recorder struct {
	Upstream *url.URL // mis. https://api.example.com (path request klien dipertahankan)
	Key      string
	Price    Price
	// CapUSD: batas keras (0 = tanpa batas). Bila Ledger ada, cap efektif = min(CapUSD, Ledger.CapUSD)
	// dan biaya terpakai = total ledger lintas run. Request di atas cap ditolak 402 tanpa diteruskan.
	CapUSD float64
	Ledger *Ledger
	Client *http.Client
	// Background menerima rekaman request di luar task aktif (mis. daemon agent yang masih
	// hidup); nil = hanya dihitung biayanya.
	Background io.Writer

	mu       sync.Mutex
	w        io.Writer
	agent    string
	task     string
	seq      int
	spent    float64
	reserved float64
	usage    Usage
	errs     []string
	bg       Usage
	bgErrs   []string
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

// Spent adalah total USD terpakai: total ledger lintas run bila Ledger ada, selain itu sejak
// proxy dibuat.
func (r *Recorder) Spent() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spentLocked()
}

func (r *Recorder) spentLocked() float64 {
	if r.Ledger != nil {
		return r.Ledger.Spent()
	}
	return r.spent
}

// Cap adalah cap efektif (0 = tanpa batas).
func (r *Recorder) Cap() float64 {
	c := r.CapUSD
	if r.Ledger != nil && (c <= 0 || r.Ledger.CapUSD < c) {
		c = r.Ledger.CapUSD
	}
	return c
}

// reserve memesan proyeksi terburuk request sebelum diteruskan; gagal bila terpakai + reservasi
// berjalan + proyeksi > cap.
func (r *Recorder) reserve(rec *Record, body []byte) error {
	capUSD := r.Cap()
	proj := r.Price.Worst(body)
	r.mu.Lock()
	defer r.mu.Unlock()
	if capUSD > 0 && r.spentLocked()+r.reserved+proj > capUSD {
		return fmt.Errorf("budget cap %.2f USD: terpakai %.4f + reservasi %.4f + proyeksi %.4f melebihi cap",
			capUSD, r.spentLocked(), r.reserved, proj)
	}
	r.reserved += proj
	rec.ReservedUSD = proj
	return nil
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
	billable := req.Method == http.MethodPost
	if billable && !billablePath(req.URL.Path) {
		r.fail(w, &rec, http.StatusBadRequest, "POST "+req.URL.Path+" tidak dikenal: biayanya tidak bisa dihitung")
		return
	}
	// Kunci upstream nyata = biaya nyata: tanpa ledger lintas proses cap ADR-062 bisa dilewati (ADR-085).
	if billable && r.Key != "" && r.Ledger == nil {
		r.fail(w, &rec, http.StatusInternalServerError, "ledger wajib untuk Recorder berkunci upstream (ADR-085)")
		return
	}
	if billable && r.Cap() > 0 && !r.Price.Valid() {
		r.fail(w, &rec, http.StatusInternalServerError, "harga input/output wajib > 0 bila cap aktif (ADR-075)")
		return
	}
	if billable {
		if err := r.reserve(&rec, body); err != nil {
			r.fail(w, &rec, http.StatusPaymentRequired, err.Error())
			return
		}
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

	// Klien (agent) yang membatalkan request-nya sendiri — mis. panggilan judul sesi saat
	// proses agent keluar — bukan kegagalan harness; direkam sebagai client_aborted.
	clientGone := func() bool { return req.Context().Err() != nil }
	resp, err := r.Client.Do(out)
	if err != nil {
		if clientGone() {
			rec.Status, rec.ClientAborted, rec.Note = 499, true, "upstream: "+err.Error()
			r.write(&rec)
			return
		}
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
			if _, werr := w.Write(chunk[:n]); werr != nil && rec.Note == "" {
				rec.Note = "client write: " + werr.Error()
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				rec.Note = "upstream read: " + rerr.Error()
			}
			break
		}
	}
	if rec.Note != "" {
		if clientGone() {
			rec.ClientAborted = true
		} else {
			rec.Error, rec.Note = rec.Note, ""
		}
	}
	rec.RespBody = buf.String()

	if billable && resp.StatusCode == http.StatusOK {
		// usage tanpa prompt_tokens pada request non-kosong = usage tidak dapat dipercaya (AUD-037 butir 6).
		if u, ok := ParseUsage(buf.Bytes(), rec.Stream); ok && (u.PromptTokens > 0 || len(body) == 0) {
			u.CostUSD = r.Price.Cost(u)
			rec.Usage = &u
		} else if !rec.ClientAborted {
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
	if rec.ClientAborted && billablePath(rec.Path) {
		usage.AbortedCalls++
	}
	if rec.Usage != nil {
		usage.Add(*rec.Usage)
	}
	// Biaya: usage nyata; bila usage tidak diketahui (usage_missing, abort, error transport) =
	// proyeksi terburuk yang direservasi. Respons non-200 lengkap dari upstream = 0 (tidak ditagih).
	charge := 0.0
	switch {
	case rec.Usage != nil:
		charge = rec.Usage.CostUSD
	case rec.ReservedUSD > 0 && (rec.UsageMissing || rec.ClientAborted || rec.Error != ""):
		charge, rec.WorstCase = rec.ReservedUSD, true
	}
	r.reserved -= rec.ReservedUSD
	if charge > 0 {
		rec.ChargedUSD = charge
		r.spent += charge
		if r.Ledger != nil {
			if err := r.Ledger.Add(charge); err != nil {
				*errs = append(*errs, "ledger: "+err.Error())
			}
		}
	}
	if rec.Error != "" {
		*errs = append(*errs, fmt.Sprintf("seq %d %s: %s", rec.Seq, rec.Path, rec.Error))
	}
	if rec.UsageMissing {
		*errs = append(*errs, fmt.Sprintf("seq %d %s: usage_missing (usage tidak ada atau prompt_tokens = 0)", rec.Seq, rec.Path))
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
	// Chat Completions.
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	// Responses API.
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// ParseUsage mengambil usage dari body JSON atau, bila stream, dari chunk SSE terakhir
// yang membawa usage (Chat Completions `usage` dengan stream_options.include_usage, atau
// Responses API `response.usage` pada event `response.completed`).
func ParseUsage(body []byte, stream bool) (Usage, bool) {
	var found *wireUsage
	parse := func(b []byte) {
		var m struct {
			Usage    *wireUsage `json:"usage"`
			Response *struct {
				Usage *wireUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal(b, &m) != nil {
			return
		}
		if m.Usage != nil {
			found = m.Usage
		} else if m.Response != nil && m.Response.Usage != nil {
			found = m.Response.Usage
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
		PromptTokens:     found.PromptTokens + found.InputTokens,
		CompletionTokens: found.CompletionTokens + found.OutputTokens,
		CachedTokens:     found.PromptTokensDetails.CachedTokens + found.InputTokensDetails.CachedTokens,
		ReasoningTokens:  found.CompletionTokensDetails.ReasoningTokens + found.OutputTokensDetails.ReasoningTokens,
	}, true
}
