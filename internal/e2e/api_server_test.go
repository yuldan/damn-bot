package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestF7APIServerPrompt runs the real binary's api-server against the fake
// provider: one HTTP prompt reaches the loop and its answer comes back in the
// OpenAI envelope.
func TestF7APIServerPrompt(t *testing.T) {
	e := New(t, providertest.Text("pong"))
	bin := binPaths.main
	if bin == "" {
		t.Fatal("main binary missing")
	}
	args := []string{
		"api-server", "--addr", "127.0.0.1:0", "--model", modelRef,
		"--base-url", e.Server.URL, "--api-key", "e2e", "--offline",
		"--data-dir", e.DataDir, "--config", e.cfg,
	}
	cmd := exec.CommandContext(t.Context(), bin, args...) // #nosec G204 -- this package's temp build
	cmd.Dir = e.Workdir
	cmd.Env = append(e.childEnv(), "DAMNBOT_API_TOKEN=e2e-token")
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr apiSyncBuffer
	cmd.Stderr = &stderr
	if serr := cmd.Start(); serr != nil {
		t.Fatalf("start: %v", serr)
	}
	addr, err := waitListen(t, pipe, &stdout, &stderr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()    //nolint:errcheck // best-effort kill; the process may already be gone
		_, _ = cmd.Process.Wait() //nolint:errcheck // reaps the killed child
	})
	body := `{"model":"` + modelRef + `","messages":[{"role":"user","content":"ping"}]}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer e2e-token")
	req.Header.Set("Content-Type", "application/json")
	client := netx.New(netx.Config{}).Client("e2e-api-server", netx.Background)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v (stderr %s)", err, stderr.String())
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // body fully read below
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("read body: %v", rerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s stderr = %s", resp.StatusCode, raw, stderr.String())
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "pong" {
		t.Fatalf("response = %s", raw)
	}
	if !strings.Contains(string(e.Requests()[0]), "ping") {
		t.Errorf("provider request lost the prompt: %s", e.Requests()[0])
	}
}

func waitListen(t *testing.T, pipe io.ReadCloser, stdout, stderr *apiSyncBuffer) (string, error) {
	t.Helper()
	br := bufio.NewReader(pipe)
	lineCh := make(chan string, 1)
	go func() {
		line, err := br.ReadString('\n')
		if err == nil {
			lineCh <- line
		}
		_, _ = io.Copy(stdout, br) //nolint:errcheck // stream copy ends when the pipe closes
	}()
	select {
	case line := <-lineCh:
		const prefix = "api-server listening on "
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !ok {
			return "", fmt.Errorf("unexpected first line %q", line)
		}
		addr, _, _ := strings.Cut(rest, " ")
		return addr, nil
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("server never reported listening\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}
}

type apiSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *apiSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p) //nolint:wrapcheck // bytes.Buffer.Write never returns a non-nil error
}

func (b *apiSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
