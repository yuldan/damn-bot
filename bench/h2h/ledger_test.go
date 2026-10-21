package h2h

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testPrice: tarif > 0 (Price.Valid) agar cap aktif.
var testPrice = Price{Input: 1, CachedInput: 0.5, Output: 2}

// upstreamWith menjalankan upstream palsu yang selalu membalas body dan menghitung hit.
func upstreamWith(t *testing.T, status int, body string) (*url.URL, *http.Client, *int) {
	t.Helper()
	hits := new(int)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*hits++
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(up.Close)
	u, _ := url.Parse(up.URL)
	return u, up.Client(), hits
}

func post(t *testing.T, base, path, body string) int {
	t.Helper()
	resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// waitRecorded menunggu sampai proxy selesai menulis n record: klien bisa menerima respons
// sebelum handler mencatat record/error (write terjadi setelah body diteruskan; biaya sudah
// di-settle sebelumnya).
func waitRecorded(t *testing.T, r *Recorder, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		seq := r.seq
		r.mu.Unlock()
		if seq >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy mencatat %d record, want %d", seq, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLedgerCapAcrossRuns: biaya tiap run tersimpan di ledger; run berikutnya (Recorder baru,
// seperti proses baru) membaca total itu, dan ditolak tanpa forward begitu terpakai + proyeksi
// melebihi cap (ADR-075). Cap di file tidak bisa dinaikkan lewat argumen.
func TestLedgerCapAcrossRuns(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "spend.json")
	// 600.000 prompt token × US$1/M = US$0,60 per call.
	u, c, hits := upstreamWith(t, 200, `{"usage":{"prompt_tokens":600000,"completion_tokens":0}}`)
	body := `{"max_tokens":100}`

	// Run 1: 0 + proyeksi kecil ≤ 1,00 → diteruskan (0,60). Run 2: 0,60 + proyeksi ≤ 1,00 →
	// diteruskan (total 1,20; cap mencegah request baru, bukan biaya yang sudah berjalan).
	for run := range 2 {
		l, err := OpenLedger(path, 1.00)
		if err != nil {
			t.Fatal(err)
		}
		px := httptest.NewServer(&Recorder{Upstream: u, Price: testPrice, Ledger: l, Client: c})
		got := post(t, px.URL, "/v1/chat/completions", body)
		px.Close()
		if got != http.StatusOK {
			t.Fatalf("run %d: status %d", run+1, got)
		}
	}
	l, err := OpenLedger(path, 5.00)
	if err != nil {
		t.Fatal(err)
	}
	if l.CapUSD != 1.00 {
		t.Fatalf("cap ledger berubah: %v", l.CapUSD)
	}
	if l.Spent() != 1.20 || *hits != 2 {
		t.Fatalf("spent=%v hits=%d, want 1.20/2", l.Spent(), *hits)
	}
	// Run 3: terpakai 1,20 ≥ cap → ditolak tanpa forward.
	rec := &Recorder{Upstream: u, Price: testPrice, Ledger: l, Client: c}
	px := httptest.NewServer(rec)
	defer px.Close()
	if got := post(t, px.URL, "/v1/chat/completions", body); got != http.StatusPaymentRequired || *hits != 2 {
		t.Fatalf("status=%d hits=%d, want 402/2", got, *hits)
	}
}

// TestReservationRejectsOverCap: proyeksi terburuk (tanpa max_tokens → DefaultMaxOutput) yang
// melebihi sisa cap ditolak sebelum forward.
func TestReservationRejectsOverCap(t *testing.T) {
	t.Parallel()
	u, c, hits := upstreamWith(t, 200, `{"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
	// Proyeksi: 32768 output × US$2/M ≈ 0,066 > cap 0,05.
	rec := &Recorder{Upstream: u, Price: testPrice, CapUSD: 0.05, Client: c}
	px := httptest.NewServer(rec)
	defer px.Close()
	if got := post(t, px.URL, "/v1/chat/completions", `{}`); got != http.StatusPaymentRequired || *hits != 0 {
		t.Fatalf("status=%d hits=%d, want 402/0", got, *hits)
	}
	if got := post(t, px.URL, "/v1/chat/completions", `{"max_tokens":100}`); got != http.StatusOK || *hits != 1 {
		t.Fatalf("status=%d hits=%d, want 200/1", got, *hits)
	}
}

// TestPriceRequired: cap aktif dengan tarif output 0 → error harness, tidak pernah diteruskan.
// Tarif cached 0 sah (ADR-085).
func TestPriceRequired(t *testing.T) {
	t.Parallel()
	u, c, hits := upstreamWith(t, 200, `{"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
	rec := &Recorder{Upstream: u, Price: Price{Input: 1}, CapUSD: 1, Client: c}
	px := httptest.NewServer(rec)
	defer px.Close()
	rec.Begin("a", "T", io.Discard)
	got := post(t, px.URL, "/v1/chat/completions", `{"max_tokens":1}`)
	waitRecorded(t, rec, 1)
	_, errs := rec.End()
	if got != http.StatusInternalServerError || *hits != 0 || len(errs) != 1 {
		t.Fatalf("status=%d hits=%d errs=%v", got, *hits, errs)
	}
	if !(Price{Input: 1, Output: 1}).Valid() {
		t.Fatal("cached 0 harus sah")
	}
}

// TestLedgerRequiredWithKey: Recorder dengan kunci upstream tanpa ledger menolak request (ADR-085).
func TestLedgerRequiredWithKey(t *testing.T) {
	t.Parallel()
	u, c, hits := upstreamWith(t, 200, `{"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
	rec := &Recorder{Upstream: u, Key: "k", Price: testPrice, CapUSD: 1, Client: c}
	px := httptest.NewServer(rec)
	defer px.Close()
	if got := post(t, px.URL, "/v1/chat/completions", `{"max_tokens":1}`); got != http.StatusInternalServerError || *hits != 0 {
		t.Fatalf("status=%d hits=%d, want 500/0", got, *hits)
	}
}

// TestLedgerSharedAcrossInstances: dua Ledger pada file yang sama (flock per open file
// description, perilakunya sama dengan dua proses) me-reserve dan settle bersamaan → tidak ada
// update hilang, tidak ada reservasi tersisa, dan masing-masing melihat total terbaru.
func TestLedgerSharedAcrossInstances(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "spend.json")
	a, err := OpenLedger(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenLedger(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for _, l := range []*Ledger{a, b} {
		go func() {
			for range 50 {
				id, rerr := l.Reserve(0.25, 0)
				if rerr == nil {
					rerr = l.Settle(id, 0.25)
				}
				if rerr != nil {
					done <- rerr
					return
				}
			}
			done <- nil
		}()
	}
	for range 2 {
		if derr := <-done; derr != nil {
			t.Fatal(derr)
		}
	}
	if a.Spent() != 25 || b.Spent() != 25 {
		t.Fatalf("spent a=%v b=%v, want 25", a.Spent(), b.Spent())
	}
	if f, lerr := a.load(); lerr != nil || len(f.ReservedUSD) != 0 {
		t.Fatalf("reservasi tersisa: %+v %v", f, lerr)
	}
}

// TestUsageMissing: 200 tanpa usage, atau prompt_tokens = 0 pada request non-kosong → error
// harness usage_missing dan biaya = proyeksi terburuk (bukan 0).
func TestUsageMissing(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"absent":      `{"choices":[]}`,
		"zero_prompt": `{"usage":{"prompt_tokens":0,"completion_tokens":5}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, c, _ := upstreamWith(t, 200, body)
			rec := &Recorder{Upstream: u, Price: testPrice, CapUSD: 1, Client: c}
			px := httptest.NewServer(rec)
			defer px.Close()
			req := `{"max_tokens":1000,"messages":[{"role":"user","content":"hi"}]}`
			rec.Begin("a", "T", io.Discard)
			post(t, px.URL, "/v1/chat/completions", req)
			waitRecorded(t, rec, 1)
			_, errs := rec.End()
			want := testPrice.Worst([]byte(req))
			if len(errs) != 1 || !strings.Contains(errs[0], "usage_missing") || rec.Spent() != want {
				t.Fatalf("errs=%v spent=%v want %v", errs, rec.Spent(), want)
			}
		})
	}
}

// TestUnknownPathRejected: POST ke path yang biayanya tidak dihitung ditolak; /responses dihitung.
func TestUnknownPathRejected(t *testing.T) {
	t.Parallel()
	u, c, hits := upstreamWith(t, 200, `{"usage":{"input_tokens":1000000,"output_tokens":0}}`)
	rec := &Recorder{Upstream: u, Price: testPrice, CapUSD: 5, Client: c}
	px := httptest.NewServer(rec)
	defer px.Close()
	if got := post(t, px.URL, "/v1/embeddings", `{}`); got != http.StatusBadRequest || *hits != 0 {
		t.Fatalf("embeddings: status=%d hits=%d", got, *hits)
	}
	got := post(t, px.URL, "/v1/responses", `{"max_output_tokens":1}`)
	// Biaya sudah di-settle sebelum respons sampai ke klien: tidak perlu menunggu record.
	if got != http.StatusOK || rec.Spent() != 1 {
		t.Fatalf("responses: status=%d spent=%v, want 200/1", got, rec.Spent())
	}
}

// TestConcurrentReservationsHoldCap: n request paralel mendekati cap → total proyeksi request yang
// diteruskan tidak pernah melebihi cap, juga bila request masuk lewat dua Ledger pada file yang
// sama (= dua proses), dan setiap request yang diteruskan di-settle (biaya tidak hilang, reservasi
// tidak tersisa). Upstream menahan semua request yang diteruskan sampai semua keputusan
// terima/tolak selesai, sehingga tidak ada settle yang mendahului keputusan (deterministik).
func TestConcurrentReservationsHoldCap(t *testing.T) {
	t.Parallel()
	const (
		n      = 8
		capUSD = 0.007
		body   = `{"max_tokens":1000}`
		admit  = 3 // proyeksi (10 + 1000×2)/1e6 = 0,00201: 3 × 0,00201 ≤ 0,007 < 4 × 0,00201
	)
	proj := testPrice.Worst([]byte(body))
	if admit*proj > capUSD || (admit+1)*proj <= capUSD {
		t.Fatalf("proyeksi %v tidak cocok dengan admit %d", proj, admit)
	}
	cost := testPrice.Cost(Usage{PromptTokens: 1000}) // ≤ proyeksi
	for _, mode := range []string{"memory", "one-ledger", "two-ledgers"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			arrived := make(chan struct{}, n)
			release := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				arrived <- struct{}{}
				<-release
				_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1000,"completion_tokens":0}}`)
			}))
			t.Cleanup(up.Close)
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock() // sebelum Close server (cleanup) agar handler tidak macet saat gagal
			u, _ := url.Parse(up.URL)
			path := filepath.Join(t.TempDir(), "spend.json")
			newRec := func() *Recorder {
				rec := &Recorder{Upstream: u, Price: testPrice, Client: up.Client()}
				if mode == "memory" {
					rec.CapUSD = capUSD
					return rec
				}
				l, err := OpenLedger(path, capUSD)
				if err != nil {
					t.Fatal(err)
				}
				rec.Ledger = l
				return rec
			}
			recs := []*Recorder{newRec()}
			if mode == "two-ledgers" {
				recs = append(recs, newRec())
			}
			var urls []string
			for _, rec := range recs {
				px := httptest.NewServer(rec)
				t.Cleanup(px.Close)
				urls = append(urls, px.URL)
			}

			status := make(chan int, n)
			for i := range n {
				go func() {
					resp, err := http.Post(urls[i%len(urls)]+"/v1/chat/completions", "application/json", strings.NewReader(body))
					if err != nil {
						status <- -1
						return
					}
					resp.Body.Close()
					status <- resp.StatusCode
				}()
			}
			admitted, rejected := 0, 0
			timeout := time.After(10 * time.Second)
			for admitted+rejected < n {
				select {
				case <-arrived:
					admitted++
				case s := <-status:
					if s != http.StatusPaymentRequired {
						t.Fatalf("status %d sebelum upstream dilepas", s)
					}
					rejected++
				case <-timeout:
					t.Fatalf("macet: admitted=%d rejected=%d", admitted, rejected)
				}
			}
			unblock()
			for range admitted {
				if s := <-status; s != http.StatusOK {
					t.Fatalf("status %d, want 200", s)
				}
			}
			if admitted != admit {
				t.Fatalf("admitted %d, want %d (cap %v, proyeksi %v)", admitted, admit, capUSD, proj)
			}

			want := 0.0
			for range admit {
				want += cost
			}
			for _, rec := range recs {
				if got := rec.Spent(); got != want {
					t.Fatalf("spent %v, want %v", got, want)
				}
			}
			if mode == "memory" {
				recs[0].mu.Lock()
				held := recs[0].reserved
				recs[0].mu.Unlock()
				if math.Abs(held) > 1e-12 {
					t.Fatalf("reservasi tersisa %v", held)
				}
				return
			}
			if f, err := recs[0].Ledger.load(); err != nil || len(f.ReservedUSD) != 0 {
				t.Fatalf("reservasi tersisa: %+v %v", f, err)
			}
		})
	}
}

// TestSettledBeforeResponse: biaya nyata sudah tercatat di ledger saat klien menerima header
// respons non-stream, dan saat klien mencapai akhir body stream. Upstream sengaja menahan
// koneksi setelah usage terkirim (respons yang ditutup lambat).
func TestSettledBeforeResponse(t *testing.T) {
	t.Parallel()
	usage := `{"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":0}}`
	for name, tc := range map[string]struct {
		req, resp string
		stream    bool
	}{
		"json": {`{"max_tokens":10}`, usage, false},
		"sse":  {`{"max_tokens":10,"stream":true}`, "data: " + usage + "\n\ndata: [DONE]\n\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.resp)
				w.(http.Flusher).Flush()
				time.Sleep(50 * time.Millisecond)
			}))
			t.Cleanup(up.Close)
			u, _ := url.Parse(up.URL)
			l, err := OpenLedger(filepath.Join(t.TempDir(), "spend.json"), 1)
			if err != nil {
				t.Fatal(err)
			}
			px := httptest.NewServer(&Recorder{Upstream: u, Price: testPrice, Ledger: l, Client: up.Client()})
			t.Cleanup(px.Close)
			resp, err := http.Post(px.URL+"/v1/chat/completions", "application/json", strings.NewReader(tc.req))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if tc.stream {
				if _, err := io.ReadAll(resp.Body); err != nil {
					t.Fatal(err)
				}
			}
			if got, want := l.Spent(), testPrice.Cost(Usage{PromptTokens: 1000}); got != want {
				t.Fatalf("spent %v saat klien menerima respons, want %v", got, want)
			}
		})
	}
}

// TestLedgerReclaimsDeadReservation: reservasi milik proses yang sudah mati (crash di antara
// forward dan settle) dibebankan sebagai biaya terburuk saat ledger dibuka; reservasi proses hidup
// tetap dihitung terhadap cap. Setelah semua di-settle, file kembali tanpa reserved_usd.
func TestLedgerReclaimsDeadReservation(t *testing.T) {
	t.Parallel()
	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	live := strconv.Itoa(os.Getpid()) + "-live"
	path := filepath.Join(t.TempDir(), "spend.json")
	file := fmt.Sprintf(`{"cap_usd":1,"spent_usd":0.5,"updated":"2026-10-21T00:00:00Z","reserved_usd":{"%d-dead":0.25,%q:0.125}}`,
		dead.Process.Pid, live)
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLedger(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if l.Spent() != 0.75 || l.CapUSD != 1 {
		t.Fatalf("spent=%v cap=%v, want 0.75/1", l.Spent(), l.CapUSD)
	}
	// 0,75 + reservasi hidup 0,125 + 0,2 > 1.
	if _, err := l.Reserve(0.2, 0); err == nil || !strings.Contains(err.Error(), "budget cap") {
		t.Fatalf("reserve melewati cap: %v", err)
	}
	if err := l.Settle(live, 0.0625); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if l.Spent() != 0.8125 || strings.Contains(string(b), "reserved_usd") {
		t.Fatalf("spent=%v file=%s", l.Spent(), b)
	}
}

// TestSettleFailureHalts: settle yang gagal menulis ledger membiarkan reservasinya di file dan
// menghentikan request berbayar berikutnya (402 tanpa forward), walau ledger sudah pulih (ADR-091
// NEW2-24).
func TestSettleFailureHalts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "spend.json")
	l, err := OpenLedger(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	var good []byte
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		good, _ = os.ReadFile(path) // berisi reservasi request ini
		_ = os.WriteFile(path, []byte("{"), 0o600)
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1000,"completion_tokens":0}}`)
	}))
	t.Cleanup(up.Close)
	u, _ := url.Parse(up.URL)
	rec := &Recorder{Upstream: u, Price: testPrice, Ledger: l, Client: up.Client()}
	px := httptest.NewServer(rec)
	t.Cleanup(px.Close)
	rec.Begin("a", "T", io.Discard)
	if got := post(t, px.URL, "/v1/chat/completions", `{"max_tokens":10}`); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	waitRecorded(t, rec, 1)
	if _, errs := rec.End(); len(errs) != 1 || !strings.HasPrefix(errs[0], "ledger: ") {
		t.Fatalf("errs=%v", errs)
	}
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := post(t, px.URL, "/v1/chat/completions", `{"max_tokens":10}`); got != http.StatusPaymentRequired || hits != 1 {
		t.Fatalf("status=%d hits=%d, want 402/1", got, hits)
	}
	if f, err := l.load(); err != nil || len(f.ReservedUSD) != 1 {
		t.Fatalf("reservasi request pertama harus tetap ada: %+v %v", f, err)
	}
}
