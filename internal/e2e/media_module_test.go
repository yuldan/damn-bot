package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// writeMediaConfig writes the [modules.media] section (and the optional extra
// TOML) used by the media scenarios.
func writeMediaConfig(t *testing.T, e *Env, enabled bool, extra string) {
	t.Helper()
	body := "[modules.media]\nenabled = false\n"
	if enabled {
		body = "[modules.media]\nenabled = true\n" + extra
	}
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func mediaRunArgs(e *Env, prompt string) []string {
	return []string{
		"run", "-p", prompt,
		"--model", modelRef, "--base-url", e.Server.URL, "--api-key", "e2e",
		"--data-dir", e.DataDir, "--config", e.cfg,
		"--approval", "auto", "--output", "json",
	}
}

// TestF6MediaDocReadE2E: a workspace document is read through the real module
// and committed as <untrusted source="media:doc">.
func TestF6MediaDocReadE2E(t *testing.T) {
	t.Parallel()
	e := NewTrusted(t,
		ToolCallReply("s1", "tool_search", `{"query":"doc_read"}`),
		ToolCallReply("r1", "tool_invoke", `{"name":"doc_read","arguments":{"path":"notes.txt"}}`),
		providertest.Text("done"),
	)
	if err := os.WriteFile(filepath.Join(e.Workdir, "notes.txt"), []byte("Halo media dari fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeMediaConfig(t, e, true, "")
	res := e.Exec(mediaRunArgs(e, "read the notes")...)
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) != 3 {
		t.Fatalf("provider requests = %d, want 3\nstderr:\n%s", len(reqs), res.Stderr)
	}
	transcript := string(reqs[2])
	for _, want := range []string{"Halo media dari fixture", `untrusted source=\"media:doc\"`} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript lacks %q", want)
			logTranscript(t, reqs[2])
		}
	}
}

// TestF6MediaDocWriteApprovalE2E: without an approver the file is never
// written; with a fake approver (stdin "y") it is created.
func TestF6MediaDocWriteApprovalE2E(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		stdin bool
		write bool
	}{
		{"no approver", false, false},
		{"fake approver", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := NewTrusted(t,
				ToolCallReply("s1", "tool_search", `{"query":"doc_write"}`),
				ToolCallReply("w1", "tool_invoke", `{"name":"doc_write","arguments":{"path":"out.md","content":"# hasil"}}`),
				providertest.Text("done"),
			)
			writeMediaConfig(t, e, true, "")
			args := mediaRunArgs(e, "write the note")
			var res Result
			if tc.stdin {
				res = e.ExecStdin("y\n", args...)
			} else {
				res = e.Exec(args...)
			}
			res.RequireOK(t)
			data, err := os.ReadFile(filepath.Join(e.Workdir, "out.md"))
			if (err == nil) != tc.write {
				t.Fatalf("written = %v, want %v (err %v)", err == nil, tc.write, err)
			}
			if tc.write && string(data) != "# hasil" {
				t.Fatalf("file = %q", data)
			}
			reqs := e.Requests()
			last := string(reqs[len(reqs)-1])
			if !tc.write && !strings.Contains(last, `\"code\":\"denied\"`) {
				t.Fatalf("denial missing from transcript:\n%.1200s", last)
			}
		})
	}
}

// TestF6MediaImageCapE2E: a session tool cap below the image price rejects the
// call before one vendor request is made.
func TestF6MediaImageCapE2E(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer vendor.Close()
	e := NewTrusted(t,
		ToolCallReply("s1", "tool_search", `{"query":"image_generate"}`),
		ToolCallReply("i1", "tool_invoke", `{"name":"image_generate","arguments":{"prompt":"cat","size":"1024x1024"}}`),
		providertest.Text("done"),
	)
	writeMediaConfig(t, e, true, "image_backend = \"openai\"\nimage_endpoint = \""+vendor.URL+"\"\nimage_key_env = \"MEDIA_TEST_KEY\"\n\n[budget]\ntools_session_usd = 0.000001\n")
	args := mediaRunArgs(e, "generate an image")
	res := e.ExecStdinEnv("y\n", []string{"MEDIA_TEST_KEY=test-key"}, args...)
	res.RequireOK(t)
	if got := calls.Load(); got != 0 {
		t.Fatalf("vendor calls = %d, want 0", got)
	}
	reqs := e.Requests()
	last := string(reqs[len(reqs)-1])
	if !strings.Contains(last, "budget_exhausted") {
		t.Fatalf("cap denial missing from transcript:\n%.1500s", last)
	}
}

// TestF6MediaVisionGateE2E: a text-only session rejects vision_analyze before
// the media file is read and before the provider sees an attachment. The
// local path never reaches the transcript.
func TestF6MediaVisionGateE2E(t *testing.T) {
	t.Parallel()
	e := NewTrusted(t,
		ToolCallReply("s1", "tool_search", `{"query":"vision_analyze"}`),
		ToolCallReply("v1", "tool_invoke", `{"name":"vision_analyze","arguments":{"path":"secret.png"}}`),
		providertest.Text("done"),
	)
	if err := os.WriteFile(filepath.Join(e.Workdir, "secret.png"), []byte("SENTINEL-IMAGE-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeMediaConfig(t, e, true, "")
	res := e.Exec(mediaRunArgs(e, "describe the image")...)
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) != 3 {
		t.Fatalf("provider requests = %d, want 3 (the gate must not call the model)\nstderr:\n%s", len(reqs), res.Stderr)
	}
	last := string(reqs[len(reqs)-1])
	if !strings.Contains(last, "module_unavailable") {
		t.Fatalf("vision gate denial missing from transcript:\n%.1500s", last)
	}
	for i, raw := range reqs {
		if strings.Contains(string(raw), "SENTINEL-IMAGE-BYTES") {
			t.Fatalf("media bytes reached request %d", i)
		}
	}
}

// TestF6MediaDisabledE2E: disabled keeps every media tool out of the catalog.
func TestF6MediaDisabledE2E(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("r1", "doc_read", `{"path":"x.txt"}`),
		providertest.Text("done"),
	)
	writeMediaConfig(t, e, false, "")
	res := e.Exec(mediaRunArgs(e, "read x")...)
	res.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) < 2 {
		t.Fatalf("provider requests = %d", len(reqs))
	}
	declared := webToolNames(t, reqs[0])
	for _, name := range []string{"tool_search", "tool_invoke", "doc_read", "doc_write", "image_generate", "video_generate", "vision_analyze", "video_analyze"} {
		if declared[name] {
			t.Errorf("%s advertised with media disabled: %v", name, declared)
		}
	}
	if !strings.Contains(string(reqs[1]), "unknown_tool") {
		t.Errorf("direct call was not refused:\n%.1200s", reqs[1])
	}
}
