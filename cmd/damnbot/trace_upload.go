//go:build !minimal

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
)

const (
	traceUploadDefaultEndpoint = "https://huggingface.co"
	traceUploadDefaultSecret   = "huggingface_token"
	traceUploadMaxRowsBytes    = 8 << 20
)

var traceRepoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// traceAllowed is the closed allowlist of uploadable trace fields. Anything
// else (messages, prompts, arguments, results) is dropped before the body is
// built, so no user transcript can reach the upload lane.
func traceAllowed(key string) bool {
	switch key {
	case "ts", "time", "event", "tool", "name", "status", "ok", "duration_ms",
		"elapsed_ms", "model", "input_tokens", "output_tokens", "iteration",
		"turn", "error_class":
		return true
	}
	return false
}

// debugTrace is the explicit opt-in Hugging Face trace upload of OPS-022.
// Without --upload it refuses as a usage error and sends nothing; with it, a
// sanitized metadata-only JSONL is posted through the trace-upload lane.
func debugTrace(args []string, _ string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("debug trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "trace file (required with --upload)")
	upload := fs.Bool("upload", false, "upload to Hugging Face (opt-in)")
	repo := fs.String("repo", "", "Hugging Face dataset repository owner/name")
	endpoint := fs.String("endpoint", traceUploadDefaultEndpoint, "Hugging Face endpoint (https or loopback)")
	secret := fs.String("token-secret", traceUploadDefaultSecret, "keychain entry holding the HF token")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *file == "" || !*upload {
		say(stderr, "usage: damnbot debug trace --file TRACE --upload --repo OWNER/NAME [--endpoint URL] [--token-secret NAME]\n")
		return f8ExitUsage
	}
	if !traceRepoRE.MatchString(*repo) {
		return f8CmdFail(stderr, "debug trace", f8BadArg("--repo must be owner/name"))
	}
	if err := traceEndpointOK(*endpoint); err != nil {
		return f8CmdFail(stderr, "debug trace", err)
	}
	raw, err := readTraceFile(*file)
	if err != nil {
		return f8CmdFail(stderr, "debug trace", err)
	}
	payload, err := traceSanitize(raw)
	if err != nil {
		return f8CmdFail(stderr, "debug trace", err)
	}
	token, err := traceUploadToken(context.Background(), *secret)
	if err != nil {
		return f8CmdFail(stderr, "debug trace", err)
	}
	name := "trace-" + time.Now().UTC().Format("20060102T150405Z") + ".jsonl"
	uploadURL := strings.TrimRight(*endpoint, "/") + "/api/datasets/" + *repo + "/upload/main/" + url.PathEscape(name)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctx = netx.WithAuthHeaders(ctx, "Authorization")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(payload))
	if err != nil {
		return f8CmdFail(stderr, "debug trace", f8BadArg("upload URL is invalid"))
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Authorization", "Bearer "+token)
	client := netx.New(netx.Config{NoBackground: true, Secrets: []string{token}}).Client("trace-upload", netx.Tool)
	resp, err := client.Do(req)
	if err != nil {
		return f8CmdFail(stderr, "debug trace", f8Unavail("trace-upload", "the upload request failed; nothing was retried"))
	}
	defer proxyCloseBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return f8CmdFail(stderr, "debug trace", f8Unavail("trace-upload",
			fmt.Sprintf("Hugging Face returned HTTP %d", resp.StatusCode)))
	}
	say(stdout, "uploaded trace bytes=%d repo=%s rows=metadata transcript=0\n", len(payload), cleanLine(*repo))
	return f8ExitOK
}

func readTraceFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, f8BadArg("trace file: %s", cleanLine(err.Error()))
	}
	if !fi.Mode().IsRegular() {
		return nil, f8BadArg("trace file must be a regular file")
	}
	if fi.Size() > 32<<20 {
		return nil, f8BadArg("trace file is larger than 32 MiB")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied trace file
	if err != nil {
		return nil, f8BadArg("trace file: %s", cleanLine(err.Error()))
	}
	return b, nil
}

// traceSanitize keeps only allowlisted scalar fields from JSONL rows. A
// non-JSON line and every non-allowlisted key are dropped, so the uploaded
// bytes never carry a message, prompt, argument, or result.
func traceSanitize(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	rows := 0
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		row, ok := traceRow(line)
		if !ok {
			continue
		}
		if _, err := out.Write(row); err != nil {
			continue
		}
		if err := out.WriteByte('\n'); err != nil {
			continue
		}
		rows++
	}
	if rows == 0 {
		return nil, f8BadArg("trace file has no uploadable metadata rows")
	}
	if out.Len() > traceUploadMaxRowsBytes {
		return nil, f8BadArg("sanitized trace is larger than 8 MiB")
	}
	return out.Bytes(), nil
}

// traceRow rewrites one line through the allowlist; false drops it.
func traceRow(line []byte) ([]byte, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(line, &obj) != nil {
		return nil, false
	}
	keep := make(map[string]json.RawMessage, len(obj))
	for key, val := range obj {
		if scalar, ok := traceScalar(key, val); ok {
			keep[key] = scalar
		}
	}
	if len(keep) == 0 {
		return nil, false
	}
	row, err := json.Marshal(keep)
	if err != nil {
		return nil, false
	}
	return row, true
}

func traceScalar(key string, raw json.RawMessage) (json.RawMessage, bool) {
	if !traceAllowed(key) {
		return nil, false
	}
	val := bytes.TrimSpace(raw)
	if len(val) == 0 || val[0] == '{' || val[0] == '[' {
		return nil, false
	}
	if val[0] != '"' {
		return val, true
	}
	var s string
	if json.Unmarshal(val, &s) != nil {
		return nil, false
	}
	if len(s) > 512 {
		s = s[:512] + "…"
	}
	b, err := json.Marshal(f8Redact(s))
	if err != nil {
		return nil, false
	}
	return b, true
}

// traceUploadToken resolves the HF token from the keychain entry first, then
// HF_TOKEN. The value is never returned in an error or printed.
func traceUploadToken(ctx context.Context, name string) (string, error) {
	if sec, err := secretsStore(); err == nil && name != "" {
		if v, gerr := sec.Get(ctx, name); gerr == nil && v != "" {
			return v, nil
		}
	}
	for _, env := range []string{"HF_TOKEN", "DAMNBOT_HF_TOKEN"} {
		if v := strings.TrimSpace(config.OS().Getenv(env)); v != "" {
			return v, nil
		}
	}
	return "", f8Unavail("trace-upload", "no Hugging Face token; set keychain entry "+cleanArg(name)+" or HF_TOKEN")
}

// traceEndpointOK accepts https, or loopback http for the fake test server.
func traceEndpointOK(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return f8BadArg("--endpoint is not a URL")
	}
	if u.Scheme == "https" || (u.Scheme == "http" && proxyLoopback(u.Hostname())) {
		return nil
	}
	return f8BadArg("--endpoint must be https or a loopback http URL")
}
