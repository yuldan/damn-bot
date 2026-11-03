package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// buildWebtest builds the `webtest` child once per scenario. The tag flips
// only the SSRF loopback gate (netx.TestDial + webtools.defaultPolicy), so the
// module, dispatcher, netx client and transcript stay production code.
func buildWebtest(t *testing.T) string {
	t.Helper()
	return buildE2EBin(t, "goolm,webtest", "damnbot-webtest")
}

// TestF6WebModuleE2E is the W1 end-to-end proof of the web module wiring:
// `damnbot run` with [modules.web].enabled = true advertises only the
// meta-tools, resolves web_fetch through tool_search/tool_invoke, fetches a
// loopback fixture through the real module, and commits the result to the
// provider transcript wrapped as <untrusted source="web:127.0.0.1">.
//
// The child is the `webtest` build: it flips only the SSRF loopback gate so
// the fixture is reachable; the module, dispatcher, netx client and transcript
// are production code.
func TestF6WebModuleE2E(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte("<title>Docs</title><p>Halo <b>dunia</b> dari fixture</p>")); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	e := New(t,
		ToolCallReply("s1", "tool_search", `{"query":"web_fetch"}`),
		ToolCallReply("c1", "tool_invoke", `{"name":"web_fetch","arguments":{"url":"`+srv.URL+`/docs"}}`),
		providertest.Text("done"),
	)
	e.bin = buildWebtest(t)
	writeWebConfig(t, e, true)

	res := runWebE2E(e, "fetch the fixture docs")
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) != 3 {
		t.Fatalf("provider requests = %d, want 3\nstdout:\n%s\nstderr:\n%s", len(reqs), res.Stdout, res.Stderr)
	}
	first := webToolNames(t, reqs[0])
	if !first["tool_search"] || !first["tool_invoke"] {
		t.Fatalf("deferred meta-tools not advertised: %v", first)
	}
	if first["web_fetch"] || first["web_search"] {
		t.Fatalf("deferred web schema leaked into the payload: %v", first)
	}
	transcript := string(reqs[2])
	for _, want := range []string{"Halo dunia dari fixture", `untrusted source=\"web:127.0.0.1\"`} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript lacks %q", want)
			logTranscript(t, reqs[2])
		}
	}
}

// logTranscript prints the non-system messages of one provider request.
func logTranscript(t *testing.T, raw []byte) {
	t.Helper()
	var body struct {
		Messages []struct {
			Role, Content string
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Logf("decode: %v", err)
		return
	}
	for i, m := range body.Messages {
		if m.Role == "system" {
			continue
		}
		t.Logf("message %d %s: %.800s", i, m.Role, m.Content)
	}
}

// TestF6WebModuleDisabledE2E: enabled = false keeps every web tool out of the
// deferred catalog, and a direct call is refused as unknown_tool instead of
// reaching a module.
func TestF6WebModuleDisabledE2E(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("w1", "web_fetch", `{"url":"https://example.test/"}`),
		providertest.Text("done"),
	)
	e.bin = binPaths.main
	writeWebConfig(t, e, false)

	res := runWebE2E(e, "fetch example")
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) < 2 {
		t.Fatalf("provider requests = %d, want the follow-up turn", len(reqs))
	}
	declared := webToolNames(t, reqs[0])
	for _, name := range []string{"tool_search", "tool_invoke", "web_fetch", "web_search"} {
		if declared[name] {
			t.Errorf("%s advertised with the web module disabled: %v", name, declared)
		}
	}
	if !strings.Contains(string(reqs[1]), "unknown_tool") {
		t.Errorf("direct call to a disabled tool was not refused:\n%.1200s", reqs[1])
	}
}

// writeWebConfig writes the module section used by the run.
func writeWebConfig(t *testing.T, e *Env, enabled bool) {
	t.Helper()
	body := "[modules.web]\nenabled = false\n"
	if enabled {
		body = "[modules.web]\nenabled = true\nengines = [\"fake\"]\n"
	}
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// runWebE2E invokes `run` without --offline: the webtest child may dial the
// loopback fixture while the provider stays the harness fake.
func runWebE2E(e *Env, prompt string) Result {
	return e.Exec(
		"run", "-p", prompt,
		"--model", modelRef,
		"--base-url", e.Server.URL,
		"--api-key", "e2e",
		"--data-dir", e.DataDir,
		"--config", e.cfg,
		"--output", "json",
	)
}

// webToolNames decodes the declared tool names of one provider request.
func webToolNames(t *testing.T, raw []byte) map[string]bool {
	t.Helper()
	var body struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("provider request: %v", err)
	}
	out := make(map[string]bool, len(body.Tools))
	for _, tool := range body.Tools {
		out[tool.Function.Name] = true
	}
	return out
}
