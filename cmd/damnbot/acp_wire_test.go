package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/acp"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tui"
)

type fakeACPRunner struct{}

func (fakeACPRunner) Prompt(context.Context, string, tui.Input) (<-chan tui.Update, error) {
	ch := make(chan tui.Update, 2)
	ch <- tui.Update{Kind: tui.KindTextDelta, Text: "hello"}
	ch <- tui.Update{Kind: tui.KindStop, Detail: "end_turn"}
	close(ch)
	return ch, nil
}
func (fakeACPRunner) Cancel(string) {}
func (fakeACPRunner) Close() error  { return nil }

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.b.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer: %w", err)
	}
	return n, nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func fakeServer(t *testing.T, out io.Writer) *acpServer {
	t.Helper()
	cfg := config.Default()
	cfg.Provider.Model = "openai/gpt-test"
	cfg.Approval.PreviewTimeoutS = 10
	deps := tuiDeps{cfg: &cfg, modelRef: cfg.Provider.Model, opts: &runOptions{approvalOrigin: "acp"}, red: redact.New(redact.Options{})}
	return newACPServer(&cfg, &deps, &streamWriter{w: out}, func() (string, error) { return "s1", nil },
		func(string, *tuiDeps) acpRunner { return fakeACPRunner{} })
}

func serveFrames(t *testing.T, input string) string {
	t.Helper()
	var out bytes.Buffer
	srv := fakeServer(t, &out)
	if err := srv.serve(t.Context(), strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func canonical(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	for line := range strings.SplitSeq(strings.TrimSuffix(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("canonical: %v: %s", err, line)
		}
		b.Write(mustJSON(t, v))
		b.WriteByte('\n')
	}
	return b.String()
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func golden(t *testing.T, name, input string) {
	t.Helper()
	path := filepath.Join("..", "..", "internal", "acp", "testdata", name)
	got := canonical(t, serveFrames(t, input))
	if os.Getenv("ACP_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("golden %s mismatch\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}

func TestACPGoldenPrompt(t *testing.T) {
	golden(t, "golden.jsonl", strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"hi"}]}}`,
	}, "\n")+"\n")
}

func TestACPGoldenProtocolErrors(t *testing.T) {
	golden(t, "golden-errors.jsonl", strings.Join([]string{
		`{`,
		`{"jsonrpc":"2.0","id":9,"method":"nope","params":{}}`,
		`{"jsonrpc":"2.0","id":10,"method":"session/prompt","params":{"sessionId":"missing","prompt":[]}}`,
	}, "\n")+"\n")
}

func TestACPMethodsSupported(t *testing.T) {
	out := serveFrames(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	if !strings.Contains(out, `"agentCapabilities"`) || !strings.Contains(out, `"authMethods"`) {
		t.Fatalf("initialize response = %s", out)
	}
	out = serveFrames(t, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`+"\n")
	var resp struct {
		Result struct {
			SessionID string   `json:"sessionId"`
			Modes     []string `json:"modes"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result.SessionID != "s1" || len(resp.Result.Modes) != 4 {
		t.Fatalf("session/new = %+v", resp.Result)
	}
	out = serveFrames(t, `{"jsonrpc":"2.0","id":3,"method":"session/set_mode","params":{"sessionId":"missing","modeId":"plan"}}`+"\n")
	if !strings.Contains(out, `"code":-32602`) {
		t.Fatalf("set_mode missing session = %s", out)
	}
	out = serveFrames(t, `{"jsonrpc":"2.0","id":4,"method":"session/cancel","params":{"sessionId":"s1"}}`+"\n")
	if out != "" {
		t.Fatalf("cancel should be a notification, got %s", out)
	}
	if code := acp.CodeInvalidParams; code != -32602 {
		t.Fatal("acp codes moved")
	}
}

func TestACPPermissionMapping(t *testing.T) {
	out := &syncBuffer{}
	srv := fakeServer(t, out)
	cfg := srv.cfg
	cfg.Approval.PreviewTimeoutS = 5
	srv.cfg = cfg
	reply := make(chan tui.ApprovalDecision, 1)
	go func() {
		reply <- srv.askPermission(t.Context(), "s1", &tui.ApprovalRequest{
			Tool: "write_file", Preview: "diff", Origin: "acp", Effect: "write", Args: `{}`, NoSession: true,
		})
	}()
	waitForLine(t, out)
	if !srv.routeResponse([]byte(`{"jsonrpc":"2.0","id":"perm-1","result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}`)) {
		t.Fatal("permission response not routed")
	}
	if got := <-reply; got != tui.ApprovalAllowOnce {
		t.Fatalf("decision = %q, want allow_once", got)
	}
}

func TestACPPermissionTimeoutDenies(t *testing.T) {
	out := &syncBuffer{}
	srv := fakeServer(t, out)
	srv.cfg.Approval.PreviewTimeoutS = 1
	reply := make(chan tui.ApprovalDecision, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	go func() {
		reply <- srv.askPermission(ctx, "s1", &tui.ApprovalRequest{
			Tool: "write_file", Preview: "diff", Origin: "acp", Effect: "write", Args: `{}`,
		})
	}()
	if got := <-reply; got != tui.ApprovalDeny {
		t.Fatalf("timeout decision = %q, want deny", got)
	}
}

func TestACPNoApproverDenies(t *testing.T) {
	b := &approvalBridge{}
	reply := make(chan tui.ApprovalDecision, 1)
	go func() { reply <- mustDecision(b) }()
	if got := <-reply; got != tui.ApprovalDeny {
		t.Fatalf("no approver = %q, want deny", got)
	}
}

func mustDecision(b *approvalBridge) tui.ApprovalDecision {
	d, err := b.ask(context.Background(), &tui.ApprovalRequest{Tool: "write_file"})
	if err != nil {
		return tui.ApprovalDeny
	}
	return d
}

func waitForLine(t *testing.T, out interface{ String() string }) {
	t.Helper()
	for range 100 {
		if strings.Contains(out.String(), "session/request_permission") {
			return
		}
		<-time.After(5 * time.Millisecond)
	}
	t.Fatalf("no permission frame written: %q", out.String())
}
