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
func (fakeACPRunner) Cancel(string)            {}
func (fakeACPRunner) Active(string) bool       { return false }
func (fakeACPRunner) SetApproval(string) error { return nil }
func (fakeACPRunner) Close() error             { return nil }

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

func TestACPInitializeProtocol(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request string
	}{
		{"supported", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`},
		{"unsupported", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":99}}`},
		{"absent", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := serveFrames(t, tc.request+"\n")
			var resp struct {
				Result struct {
					ProtocolVersion int            `json:"protocolVersion"`
					AgentInfo       map[string]any `json:"agentInfo"`
					AuthMethods     []any          `json:"authMethods"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Result.ProtocolVersion != acpProtocolVersion {
				t.Fatalf("protocolVersion = %d, want %d", resp.Result.ProtocolVersion, acpProtocolVersion)
			}
			if resp.Result.AgentInfo["name"] != "damnbot" || resp.Result.AgentInfo["version"] == "" {
				t.Fatalf("agentInfo = %+v", resp.Result.AgentInfo)
			}
			if resp.Result.AuthMethods == nil {
				t.Fatal("authMethods missing")
			}
		})
	}
}

func TestACPMethodsSupported(t *testing.T) {
	out := serveFrames(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	if !strings.Contains(out, `"agentCapabilities"`) || !strings.Contains(out, `"authMethods"`) {
		t.Fatalf("initialize response = %s", out)
	}
	out = serveFrames(t, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`+"\n")
	var resp struct {
		Result struct {
			SessionID string `json:"sessionId"`
			Modes     struct {
				CurrentModeID  string `json:"currentModeId"`
				AvailableModes []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"availableModes"`
			} `json:"modes"`
			Models struct {
				CurrentModelID  string `json:"currentModelId"`
				AvailableModels []struct {
					ModelID string `json:"modelId"`
					Name    string `json:"name"`
				} `json:"availableModels"`
			} `json:"models"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result.SessionID != "s1" || len(resp.Result.Modes.AvailableModes) != 4 || resp.Result.Modes.CurrentModeID != "ask" {
		t.Fatalf("session/new = %+v", resp.Result)
	}
	if resp.Result.Models.CurrentModelID != "openai/gpt-test" || len(resp.Result.Models.AvailableModels) != 1 || resp.Result.Models.AvailableModels[0].ModelID != "openai/gpt-test" {
		t.Fatalf("models = %+v", resp.Result.Models)
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

// chanWriter hands each written frame to a test channel.
type chanWriter struct{ ch chan []byte }

func (w *chanWriter) WriteFrame(_ context.Context, frame []byte) error {
	w.ch <- append([]byte(nil), frame...)
	return nil
}

// acpPipe drives one server over an io.Pipe, so a test can answer a
// permission request while the turn is running.
type acpPipe struct {
	t      *testing.T
	in     *io.PipeWriter
	frames chan []byte
}

func startACPPipe(t *testing.T, runner acpRunner) *acpPipe {
	t.Helper()
	cfg := config.Default()
	cfg.Provider.Model = "openai/gpt-test"
	cfg.Approval.PreviewTimeoutS = 1
	deps := tuiDeps{cfg: &cfg, modelRef: cfg.Provider.Model, opts: &runOptions{approvalOrigin: "acp"}, red: redact.New(redact.Options{}), home: t.TempDir()}
	if runner == nil {
		runner = fakeACPRunner{}
	}
	frames := make(chan []byte, 64)
	srv := newACPServer(&cfg, &deps, &chanWriter{ch: frames}, func() (string, error) { return "s1", nil },
		func(string, *tuiDeps) acpRunner { return runner })
	inR, inW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.serve(t.Context(), inR); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		if err := inW.Close(); err != nil {
			t.Errorf("close stdin: %v", err)
		}
		<-done
	})
	return &acpPipe{t: t, in: inW, frames: frames}
}

func (p *acpPipe) send(v any) {
	p.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		p.t.Fatal(err)
	}
}

func (p *acpPipe) frame() map[string]any {
	p.t.Helper()
	select {
	case b := <-p.frames:
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			p.t.Fatalf("frame %s: %v", b, err)
		}
		return v
	case <-time.After(5 * time.Second):
		p.t.Fatal("no frame within 5s")
		return nil
	}
}

// startPrompt initializes and opens session s1 for a prompt scenario.
func (p *acpPipe) startPrompt() {
	p.t.Helper()
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	if f := p.frame(); f["id"] != float64(1) || f["result"] == nil {
		p.t.Fatalf("initialize = %v", f)
	}
	p.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{}})
	if f := p.frame(); f["id"] != float64(2) || f["result"] == nil {
		p.t.Fatalf("session/new = %v", f)
	}
}

func frameError(t *testing.T, f map[string]any) (code int, errorCode string) {
	t.Helper()
	e, ok := f["error"].(map[string]any)
	if !ok {
		t.Fatalf("frame is not an error: %v", f)
	}
	c, ok := e["code"].(float64)
	if !ok {
		t.Fatalf("error code is not a number: %v", e)
	}
	code = int(c)
	if data, ok := e["data"].(map[string]any); ok {
		if v, ok := data["error_code"].(string); ok {
			errorCode = v
		}
	}
	return code, errorCode
}

// captureACPRunner records the prompt text the server hands the turn.
type captureACPRunner struct {
	mu   sync.Mutex
	seen []tui.Input
}

func (c *captureACPRunner) Prompt(_ context.Context, _ string, in tui.Input) (<-chan tui.Update, error) {
	c.mu.Lock()
	c.seen = append(c.seen, in)
	c.mu.Unlock()
	ch := make(chan tui.Update, 2)
	ch <- tui.Update{Kind: tui.KindTextDelta, Text: "hello"}
	ch <- tui.Update{Kind: tui.KindStop, Detail: "end_turn"}
	close(ch)
	return ch, nil
}
func (c *captureACPRunner) Cancel(string)            {}
func (c *captureACPRunner) Active(string) bool       { return false }
func (c *captureACPRunner) SetApproval(string) error { return nil }
func (c *captureACPRunner) Close() error             { return nil }
func (c *captureACPRunner) last(t *testing.T) tui.Input {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) == 0 {
		t.Fatal("prompt was not run")
	}
	return c.seen[len(c.seen)-1]
}

func TestACPResourceBlocks(t *testing.T) {
	runner := &captureACPRunner{}
	p := startACPPipe(t, runner)
	p.startPrompt()

	secret := "sk-abcdefghijklmnopqrstuvwxyz012345"
	p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": "s1",
		"prompt": []any{
			map[string]any{"type": "text", "text": "look at this", "x-unknown": map[string]any{"a": 1}},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///note.txt", "mimeType": "text/plain", "text": "resource body " + secret}},
			map[string]any{"type": "resource_link", "uri": "file:///other.txt", "name": "other\"><script>"},
			map[string]any{"type": "future_block", "whatever": true},
		},
	}})
	for {
		f := p.frame()
		if f["id"] == float64(3) {
			if f["error"] != nil {
				t.Fatalf("valid resource rejected: %v", f)
			}
			break
		}
	}
	in := runner.last(t)
	for _, want := range []string{"look at this", `uri="file:///note.txt"`, "resource body", "resource_link", "file:///other.txt"} {
		if !strings.Contains(in.Text, want) {
			t.Fatalf("prompt text misses %q:\n%s", want, in.Text)
		}
	}
	if strings.Contains(in.Text, secret) || !strings.Contains(in.Text, redact.SentinelPrefix) {
		t.Fatalf("resource text was not redacted:\n%s", in.Text)
	}
	if strings.Contains(in.Text, "<script>") {
		t.Fatalf("resource link name was not escaped:\n%s", in.Text)
	}

	broken := []struct {
		id    int
		block map[string]any
		code  string
	}{
		{4, map[string]any{"type": "resource", "resource": map[string]any{"text": "x"}}, "invalid_resource"},
		{5, map[string]any{"type": "resource", "resource": map[string]any{"uri": "relative", "text": "x"}}, "invalid_resource"},
		{6, map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///x", "text": "x", "blob": "eA=="}}, "invalid_resource"},
		{7, map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///x", "blob": "eA=="}}, "unsupported_resource"},
		{8, map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///x", "text": strings.Repeat("a", maxResourceChars+1)}}, "too_large"},
	}
	for _, tc := range broken {
		p.send(map[string]any{"jsonrpc": "2.0", "id": tc.id, "method": "session/prompt", "params": map[string]any{"sessionId": "s1", "prompt": []any{tc.block}}})
		code, errorCode := frameError(t, p.frame())
		if code != -32602 || errorCode != tc.code {
			t.Fatalf("%s: %d/%q, want -32602/%q", tc.block["type"], code, errorCode, tc.code)
		}
	}
}

func TestACPUnknownFieldsTolerated(t *testing.T) {
	var extra strings.Builder
	for i := range 20 {
		fmt.Fprintf(&extra, `,"unknown_%d":{"nested":[%d,"x"]}`, i, i)
	}
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true},"unknown":true}` + extra.String() + `}}`
	out := serveFrames(t, init+"\n")
	if !strings.Contains(out, `"protocolVersion":1`) || strings.Contains(out, `"error"`) {
		t.Fatalf("initialize with unknown fields = %s", out)
	}
}
