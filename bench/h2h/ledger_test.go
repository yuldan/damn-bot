package h2h

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
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

// TestPriceRequired: cap aktif dengan tarif 0 → error harness, tidak pernah diteruskan.
func TestPriceRequired(t *testing.T) {
	t.Parallel()
	u, c, hits := upstreamWith(t, 200, `{"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
	rec := &Recorder{Upstream: u, Price: Price{Input: 1, Output: 1}, CapUSD: 1, Client: c}
	px := httptest.NewServer(rec)
	defer px.Close()
	rec.Begin("a", "T", io.Discard)
	got := post(t, px.URL, "/v1/chat/completions", `{"max_tokens":1}`)
	_, errs := rec.End()
	if got != http.StatusInternalServerError || *hits != 0 || len(errs) != 1 {
		t.Fatalf("status=%d hits=%d errs=%v", got, *hits, errs)
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
	if got := post(t, px.URL, "/v1/responses", `{"max_output_tokens":1}`); got != http.StatusOK || rec.Spent() != 1 {
		t.Fatalf("responses: status=%d spent=%v, want 200/1", got, rec.Spent())
	}
}
