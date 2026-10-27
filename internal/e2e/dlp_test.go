package e2e

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// dlpCanary is shaped like a GitHub token, so both the built-in §14.3
// pattern of internal/redact and the netx DLP gate recognize it without a
// populated vault. It is a test value, never a real credential; failure
// messages must not print it.
const dlpCanary = "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestEgressDLPBodyRedacted (GAP-SEC-16): the body the binary sends to the
// provider is proof of what left the process. A secret read from the
// workspace must be redacted before that body is assembled, while text the
// redactor does not recognize stays byte-for-byte intact. Runs the real
// binary against the fake provider of the export harness; no part of the
// pipeline is exercised in-process.
func TestEgressDLPBodyRedacted(t *testing.T) {
	t.Parallel()

	t.Run("workspace context file", func(t *testing.T) {
		t.Parallel()
		e := New(t, providertest.Text("ok"))
		proxy, auth := recordingProxy(t, strings.TrimSuffix(e.Server.URL, "/v1"))
		e.WriteFile("AGENTS.md", "# agents\napi_token: "+dlpCanary+"\ncatatan: rahasia uji\n")
		res := e.Exec(
			"run", "-p", "ringkas instruksi workspace",
			"--model", modelRef,
			"--base-url", proxy.URL+"/v1",
			"--api-key", "e2e",
			"--data-dir", e.DataDir,
			"--config", e.cfg,
			"--offline",
			"--output", "json",
		)
		requireRedactedRun(t, res)
		body := e.RequestText(t, 0)
		assertBodyNoCanary(t, body, "provider request body")
		if !strings.Contains(body, "[REDACTED:github]") {
			t.Error("provider request body has no [REDACTED:github] marker; the secret was dropped or never read")
		}
		// Negative control: a line the redactor does not recognize keeps its
		// exact bytes, so redaction is targeted, not whole-body replacement.
		if !strings.Contains(body, "catatan: rahasia uji") {
			t.Errorf("unrecognized workspace text changed:\n%.200s", body)
		}
		// The credential the runtime attaches for the provider itself is an
		// allowed secret: it must cross the wire intact.
		if got := auth(); got != "Bearer e2e" {
			t.Errorf("provider credential = %q, want the runtime-owned Bearer e2e", got)
		}
	})

	t.Run("tool result in the next request", func(t *testing.T) {
		t.Parallel()
		e := New(t,
			ToolCallReply("c1", "read_file", `{"path":"kunci.txt"}`),
			providertest.Text("ok"),
		)
		e.WriteFile("kunci.txt", "token = "+dlpCanary+"\nnilai = 42\n")
		res := e.Run("baca kunci.txt lalu ringkas")
		requireRedactedRun(t, res)
		if len(e.Requests()) < 2 {
			t.Fatalf("provider requests = %d, want the follow-up turn after the tool result", len(e.Requests()))
		}
		body := e.RequestText(t, 1)
		assertBodyNoCanary(t, body, "follow-up request body")
		if !strings.Contains(body, "[REDACTED:github]") {
			t.Error("tool result reached the provider without the redaction marker")
		}
		if !strings.Contains(body, "nilai = 42") {
			t.Errorf("unrecognized tool-result text changed:\n%.400s", body)
		}
	})

	t.Run("no secret is verbatim", func(t *testing.T) {
		t.Parallel()
		e := New(t, providertest.Text("ok"))
		e.WriteFile("AGENTS.md", "# agents\ncatatan: rahasia uji\n")
		res := e.Run("ringkas instruksi workspace")
		res.RequireOK(t)
		body := e.RequestText(t, 0)
		if strings.Contains(body, "[REDACTED:") {
			t.Errorf("request body of a secret-free workspace carries a redaction marker:\n%.400s", body)
		}
		if !strings.Contains(body, "catatan: rahasia uji") {
			t.Errorf("secret-free workspace text changed:\n%.400s", body)
		}
	})
}

// TestEgressDLPSecretURLBlocked (GAP-SEC-16): a URL reference that carries a
// secret is refused by the netx DLP gate before any connection is opened; the
// fake endpoint receives nothing and the error never echoes the secret. The
// clean-URL control is refused one stage later (the tool egress policy blocks
// non-public addresses), which proves the gate itself did not fire.
func TestEgressDLPSecretURLBlocked(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("ok"))
	run := func(prompt string) Result {
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

	// Online: URL references are refused under --offline before the DLP gate.
	dirty := run("ringkas @" + e.Server.URL + "/?token=" + dlpCanary)
	if dirty.Code != 2 {
		t.Fatalf("secret URL exit = %d, want 2 (the run must fail closed)", dirty.Code)
	}
	if !strings.Contains(dirty.Stderr, "secret_egress") {
		t.Fatalf("stderr does not name secret_egress:\n%s", scrubCanary(dirty.Stderr))
	}
	if strings.Contains(dirty.Stderr, dlpCanary) || strings.Contains(dirty.Stdout, dlpCanary) {
		t.Error("the refused request echoed the secret")
	}
	if n := len(e.Requests()); n != 0 {
		t.Fatalf("provider requests = %d, want 0 before a reference is resolved", n)
	}

	clean := run("ringkas @" + e.Server.URL + "/?token=bersih")
	if clean.Code != 2 {
		t.Fatalf("clean URL exit = %d, want 2 (unreachable endpoint fails the run)", clean.Code)
	}
	if strings.Contains(clean.Stderr, "secret_egress") {
		t.Fatalf("clean URL was refused by the DLP gate:\n%s", clean.Stderr)
	}
	if !strings.Contains(clean.Stderr, "blocked_host") {
		t.Fatalf("clean URL did not reach the egress address policy:\n%s", clean.Stderr)
	}
	if n := len(e.Requests()); n != 0 {
		t.Fatalf("provider requests = %d, want 0", n)
	}
}

// requireRedactedRun fails without ever printing output that may hold the
// canary (the run itself must have redacted it; a regression must not leak it
// into test logs either).
func requireRedactedRun(t *testing.T, res Result) {
	t.Helper()
	if res.Code == 0 {
		return
	}
	if strings.Contains(res.Stdout+res.Stderr, dlpCanary) {
		t.Fatalf("run exited %d; output withheld because it carries the canary", res.Code)
	}
	t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
}

// recordingProxy forwards to the fake provider and remembers the headers of
// the first request, so a test can assert what the runtime itself attached
// (the credential is an allowed secret that must not be redacted). It lives
// here rather than in providertest to keep internal/provider's LOC budget.
func recordingProxy(t *testing.T, target string) (*httptest.Server, func() string) {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("proxy target %q: %v", target, err)
	}
	var mu sync.Mutex
	auth := ""
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.FlushInterval = -1 // stream the SSE frames as they arrive
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if auth == "" {
			auth = r.Header.Get("Authorization")
		}
		mu.Unlock()
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return auth
	}
}

// assertBodyNoCanary checks a wire body without printing it.
func assertBodyNoCanary(t *testing.T, body, where string) {
	t.Helper()
	if strings.Contains(body, dlpCanary) {
		t.Errorf("%s carries the canary unredacted (%d bytes)", where, len(body))
	}
}

// scrubCanary keeps helper failure output free of the test secret.
func scrubCanary(s string) string {
	if !strings.Contains(s, dlpCanary) {
		return s
	}
	return strings.ReplaceAll(s, dlpCanary, "[CANARY]")
}
