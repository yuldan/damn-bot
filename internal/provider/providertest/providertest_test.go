package providertest

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// exhaustEnv turns this test into the child that overruns its script.
const exhaustEnv = "PROVIDERTEST_EXHAUST_HELPER"

// TestUnexpectedRequestFailsLoudly is the negative control of the e2e
// scripts: a request beyond the script MUST fail the owning test with a
// message naming the request and the exhausted script. A silent 418 would
// let an unaccounted retry look like a green run.
func TestUnexpectedRequestFailsLoudly(t *testing.T) {
	if os.Getenv(exhaustEnv) == "1" {
		srv := New(t, nil, Text("ok"))
		// The scripted handler is called directly (httptest recorder): the
		// control needs no HTTP client and no new package edge (E-01/E-05).
		for range 2 {
			post(t, srv.srv.Config.Handler, srv.URL+"/chat/completions")
		}
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestUnexpectedRequestFailsLoudly$", "-test.count=1") // #nosec G204 G702 -- re-exec of this test binary with fixed flags
	cmd.Env = append(os.Environ(), exhaustEnv+"=1")
	out, err := cmd.CombinedOutput()
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Fatalf("script exhaustion did not fail the test (err = %v)\n%s", err, out)
	}
	for _, want := range []string{"unexpected request POST /v1/chat/completions", "script left: false"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("failure output does not name %q\n%s", want, out)
		}
	}
}

// post delivers one chat request to the scripted handler and drains the
// response.
func post(t *testing.T, h http.Handler, target string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(`{"model":"fake"}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
