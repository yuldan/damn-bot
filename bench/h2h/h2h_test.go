package h2h

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/bench/evals"
)

const upstreamKey = "sk-test-UPSTREAM-0123456789abcdef"

// TestProxyRecordsSSEAndRedacts: SSE diteruskan utuh, usage dari chunk terakhir, kunci
// dummy klien dan kunci upstream tidak pernah muncul di rekaman, upstream menerima kunci asli.
func TestProxyRecordsSSEAndRedacts(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":200,\"prompt_tokens_details\":{\"cached_tokens\":400}}}\n\n" +
		"data: [DONE]\n\n"
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	ledger, lerr := OpenLedger(filepath.Join(t.TempDir(), "spend.json"), 10)
	if lerr != nil {
		t.Fatal(lerr)
	}
	rec := &Recorder{Upstream: u, Key: upstreamKey, Price: Price{Input: 1, CachedInput: 0.1, Output: 10}, Ledger: ledger, Client: up.Client()}
	px := httptest.NewServer(rec)
	defer px.Close()

	var buf bytes.Buffer
	rec.Begin("hermes", "T1", &buf)
	req, _ := http.NewRequest("POST", px.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+DummyKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	usage, errs := rec.End()

	if string(body) != sse {
		t.Fatalf("body klien berubah: %q", body)
	}
	if gotAuth != "Bearer "+upstreamKey {
		t.Fatalf("upstream menerima %q", gotAuth)
	}
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if usage.Calls != 1 || usage.PromptTokens != 1000 || usage.CachedTokens != 400 || usage.CompletionTokens != 200 {
		t.Fatalf("usage %+v", usage)
	}
	if want := (600*1 + 400*0.1 + 200*10) / 1e6; usage.CostUSD != want {
		t.Fatalf("cost %v != %v", usage.CostUSD, want)
	}
	line := buf.String()
	if strings.Contains(line, upstreamKey) || strings.Contains(line, DummyKey) {
		t.Fatal("kunci bocor ke rekaman")
	}
	var r Record
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatal(err)
	}
	if r.ReqHeader.Get("Authorization") != Redacted || r.RespBody != sse || !r.Stream || r.Task != "T1" {
		t.Fatalf("record %+v", r)
	}
}

// TestProxyBudgetCap: setelah cap tercapai completion ditolak tanpa diteruskan.
func TestProxyBudgetCap(t *testing.T) {
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1000000,"completion_tokens":0}}`)
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	rec := &Recorder{Upstream: u, Price: Price{Input: 2, CachedInput: 1, Output: 1}, CapUSD: 1, Client: up.Client()}
	px := httptest.NewServer(rec)
	defer px.Close()
	for range 2 {
		resp, err := http.Post(px.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if hits != 1 || rec.Spent() != 2 {
		t.Fatalf("hits=%d spent=%v", hits, rec.Spent())
	}
}

// TestProxyClientAbort: request yang diputus agent direkam sebagai client_aborted, bukan
// error harness.
func TestProxyClientAbort(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	defer close(release)
	u, _ := url.Parse(up.URL)
	rec := &Recorder{Upstream: u, Client: up.Client()}
	px := httptest.NewServer(rec)
	defer px.Close()
	var buf bytes.Buffer
	rec.Begin("hermes", "T", &buf)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", px.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("request seharusnya batal")
	}
	aborted := func() int { // dibaca di bawah mutex Recorder: write() selesai sebelum lock lepas
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.usage.AbortedCalls
	}
	for deadline := time.Now().Add(5 * time.Second); aborted() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	usage, errs := rec.End()
	if len(errs) != 0 || usage.AbortedCalls != 1 || !strings.Contains(buf.String(), `"client_aborted":true`) {
		t.Fatalf("errs=%v usage=%+v rec=%s", errs, usage, buf.String())
	}
}

// TestSelectDeterministic: pilihan smoke stabil, 10 task, tanpa SWE-bench/Terminal-Bench.
func TestSelectDeterministic(t *testing.T) {
	all := func(string) (string, error) { return "/bin/true", nil }
	a, err := Select("../evals/v1", all)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Select("../evals/v1", all)
	if len(a) != 10 {
		t.Fatalf("%d task", len(a))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatal("tidak deterministik")
		}
		if a[i].Source != "internal" {
			t.Fatalf("%s bukan internal (%s)", a[i].ID, a[i].Source)
		}
	}
	none := func(string) (string, error) { return "", exec.ErrNotFound }
	if _, err := Select("../evals/v1", none); err == nil {
		t.Fatal("E1 tanpa toolchain harus gagal")
	}
}

// TestGradeReferencePasses: fixture + referensi lulus grader, fixture asli gagal.
func TestGradeReferencePasses(t *testing.T) {
	tasks, err := Select("../evals/v1", exec.LookPath)
	if err != nil {
		t.Skip("toolchain E1 tidak lengkap di mesin ini:", err)
	}
	for i := range tasks {
		tk := &tasks[i]
		t.Run(tk.ID, func(t *testing.T) {
			dir := t.TempDir()
			if err := Materialize(tk, dir); err != nil {
				t.Fatal(err)
			}
			answer := ""
			ctx := context.Background()
			if tk.Grader.Type != "answer" && Grade(ctx, tk, dir, "", nil).Pass {
				t.Fatal("fixture asli sudah lulus")
			}
			applyReference(t, tk, dir)
			if tk.Grader.Type == "answer" {
				answer = referenceAnswer(tk)
			}
			if v := Grade(ctx, tk, dir, answer, nil); !v.Pass {
				t.Fatalf("referensi gagal: %v", v.Detail)
			}
		})
	}
}

func applyReference(t *testing.T, tk *evals.Task, dir string) {
	t.Helper()
	for rel, s := range tk.Grader.Reference {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, le := range tk.Grader.Lines {
		lines := strings.Split(tk.Files[le.Path], "\n")
		lines[le.Line-1] = le.Text
		if err := os.WriteFile(filepath.Join(dir, le.Path), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(tk.Grader.ReferenceB64) > 0 {
		t.Skip("referensi b64 tidak dipakai di subset ini")
	}
}

// referenceAnswer: angka/email yang dituntut pattern, diambil dari halaman non-arsip.
func referenceAnswer(tk *evals.Task) string {
	p := tk.Grader.Pattern
	p = strings.TrimPrefix(p, "(^|\\D)")
	p = strings.TrimSuffix(p, "(\\D|$)")
	p = strings.TrimPrefix(p, "(?i)")
	return "Jawaban: " + strings.ReplaceAll(p, "\\.", ".")
}

// TestForbidden: aturan exec/fs_write E6 diterapkan pada argumen tool call.
func TestForbidden(t *testing.T) {
	calls := []ToolCall{{Name: "terminal", Arguments: `{"command":"curl x && rm -rf /tmp/a"}`}}
	if forbiddenHit(`exec:\brm\s+-[a-zA-Z]*r`, calls) == "" {
		t.Fatal("rm -rf tidak terdeteksi")
	}
	if forbiddenHit(`fs_write:^/`, calls) != "" {
		t.Fatal("terminal bukan fs_write")
	}
	if forbiddenHit(`fs_write:^/`, []ToolCall{{Name: "write_file", Arguments: `{"path":"/etc/x"}`}}) == "" {
		t.Fatal("tulis absolut tidak terdeteksi")
	}
}
