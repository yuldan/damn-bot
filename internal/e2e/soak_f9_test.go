//go:build linux

package e2e

// F9 soak + full chaos (ROADMAP F9 acceptance, SPEC §22.1:2746-2747, P18/P19/P22).
//
// TestF9Soak24h runs the real `damnbot serve` binary with three fake platform
// surfaces (Telegram polling, the generic signed webhook and the OpenAI-shaped
// api-server child) against the fake provider and counts every turn. The
// night-long profile is opt-in: DAMNBOT_SOAK_HOURS=24 selects 24 h / 1.000
// turns, `make soak` runs the 1 h / 1.000-turn nightly profile and a bare
// `DAMNBOT_SOAK=1 go test` runs the 1 minute / 50-turn CI smoke (plus the
// 100 s idle-connection settle before the FD/goroutine census). The test never
// flips the §1 aggregate in docs/F9-ACCEPTANCE.md: that needs the 24 h run on
// the release host, recorded as an artefact.
//
// The assertions are the acceptance numbers: 0 goroutine growth, Δ FD ≤ 2
// (P19/REL-12), RSS growth < 20 % (P18), every submitted message exactly once
// in the durable transcript, one turn_start/turn_end pair per message, zero
// crash_recovered events and PRAGMA integrity_check = ok (P19/P22).
//
// TestF9ChaosFull is the full chaos leg: kill -9 ×N (default 50, 1.000 with
// DAMNBOT_CHAOS_KILLS=1000), network cut through a cuttable loopback proxy,
// a genuinely full tmpfs when the runner may mount one (SQLITE_FULL injection
// otherwise, the exact driver code a full volume produces), corrupt config,
// corrupt DB, a simulated VM reboot, a clean gateway restart and the 50-client
// multi-process window. Every scenario must recover or fail closed with a typed
// error; a panic or duplicate anywhere fails the suite.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const (
	soakDefaultTurns    = 50
	soakAcceptanceTurns = 1000
	soakAcceptanceHours = 24
	soakDefaultWindow   = time.Minute
	soakRSSGrowthPct    = 20.0
	soakFDSlack         = 2
	// soakIdleSettle exceeds net/http's 90 s IdleConnTimeout. Provider and
	// Bot API keep-alive sockets (with their two server-side goroutines) are
	// not a daemon leak and must expire before the FD/goroutine census.
	soakIdleSettle = 100 * time.Second
	soakAPIKey     = "e2e-soak-api-token" // #nosec G101 -- test fixture, not a credential
	soakWebhookID  = "webhook:e2e"
)

// soakPlan is the resolved profile of one run.
type soakPlan struct {
	hours    float64
	window   time.Duration
	turns    int
	interval time.Duration
}

// soakPlanFromEnv resolves DAMNBOT_SOAK_HOURS / DAMNBOT_SOAK_TURNS. The 24 h
// profile is never implicit: a bare run is the one-minute CI smoke.
func soakPlanFromEnv(t *testing.T) soakPlan {
	t.Helper()
	hours := 0.0
	if raw := strings.TrimSpace(os.Getenv("DAMNBOT_SOAK_HOURS")); raw != "" {
		h, err := strconv.ParseFloat(raw, 64)
		if err != nil || h <= 0 {
			t.Fatalf("DAMNBOT_SOAK_HOURS=%q is not a positive number of hours", raw)
		}
		hours = h
	}
	turns := soakDefaultTurns
	if hours == soakAcceptanceHours {
		turns = soakAcceptanceTurns
	}
	if raw := strings.TrimSpace(os.Getenv("DAMNBOT_SOAK_TURNS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			t.Fatalf("DAMNBOT_SOAK_TURNS=%q is not a positive integer", raw)
		}
		turns = n
	}
	window := soakDefaultWindow
	if hours > 0 {
		window = time.Duration(hours * float64(time.Hour))
	}
	interval := max(window/time.Duration(turns), 50*time.Millisecond)
	return soakPlan{hours: hours, window: window, turns: turns, interval: interval}
}

// f9Soak is the artefact and the live measurements of one soak.
type f9Soak struct {
	Meta       f9SoakMeta       `json:"meta"`
	Surfaces   []string         `json:"surfaces"`
	Start      f9SoakSample     `json:"start"`
	End        f9SoakSample     `json:"end"`
	Deltas     f9SoakDeltas     `json:"deltas"`
	Transcript f9SoakTranscript `json:"transcript"`
	Gate       f9SoakGate       `json:"gate"`
}

type f9SoakMeta struct {
	Phase        string  `json:"phase"`
	Test         string  `json:"test"`
	Profile      string  `json:"profile"`
	Mode         string  `json:"mode"`
	Date         string  `json:"date"`
	DurationSec  float64 `json:"duration_seconds"`
	Turns        int     `json:"turns"`
	IntervalMS   float64 `json:"interval_ms"`
	Platforms    int     `json:"platforms"`
	ProviderReqs int     `json:"provider_requests"`
	Commit       string  `json:"damnbot_commit"`
}

type f9SoakSample struct {
	RSSKB      int `json:"rss_kb"`
	Goroutines int `json:"goroutines"`
	FDs        int `json:"fds"`
}

type f9SoakDeltas struct {
	Goroutines int     `json:"goroutines"`
	FDs        int     `json:"fds"`
	RSSPct     float64 `json:"rss_percent"`
}

type f9SoakTranscript struct {
	UserMessages int      `json:"user_messages"`
	TurnStarts   int      `json:"turn_starts"`
	TurnEnds     int      `json:"turn_ends"`
	Crashes      int      `json:"crash_recovered"`
	Duplicates   []string `json:"duplicates,omitempty"`
	Missing      []string `json:"missing,omitempty"`
	Integrity    string   `json:"integrity_check"`
	Placeholders int      `json:"placeholder_sends"`
	TelegramDone int      `json:"telegram_final_answers"`
	WebhookDone  int      `json:"webhook_final_answers"`
}

type f9SoakGate struct {
	Goroutines bool `json:"goroutines_ok"`
	FDs        bool `json:"fds_ok"`
	RSS        bool `json:"rss_ok"`
	Messages   bool `json:"messages_ok"`
	Integrity  bool `json:"integrity_ok"`
	NoPanic    bool `json:"no_panic"`
	Pass       bool `json:"pass"`
}

// TestF9Soak24h is the soak acceptance harness. It is not part of the fast
// package run: `make soak` sets DAMNBOT_SOAK=1. The returned artefact is a
// compact JSON log under bench/f9/results/.
func TestF9Soak24h(t *testing.T) {
	if os.Getenv("DAMNBOT_SOAK") != "1" && os.Getenv("DAMNBOT_SOAK_HOURS") == "" && os.Getenv("DAMNBOT_SOAK_TURNS") == "" {
		t.Skip("F9 soak: run `make soak` (DAMNBOT_SOAK=1)")
	}
	plan := soakPlanFromEnv(t)
	profile := soakProfile(plan)
	t.Logf("F9 soak profile=%s window=%s turns=%d interval=%s", profile, plan.window, plan.turns, plan.interval)

	started := time.Now()
	rt := startF9Soak(t)
	start, end, panicFree := soakRunTurns(t, rt, plan)
	if !panicFree {
		t.Errorf("serve panicked during the soak\nstderr:\n%s", rt.p.stderr.String())
	}
	gatewayStop(t, rt.p)
	rt.api.stop(t)
	transcript := soakVerifyTranscript(t, rt.e.DataDir, plan.turns)
	res := f9SoakResult(rt, plan, profile, started, start, end, panicFree, &transcript)
	writeF9Artifact(t, "soak", profile, &res)
	t.Logf("F9 soak: %d turns, goroutines %d→%d, fds %d→%d, rss %.1f→%.1f MB (%.1f%%), %s",
		plan.turns, start.Goroutines, end.Goroutines, start.FDs, end.FDs,
		mb(start.RSSKB), mb(end.RSSKB), res.Deltas.RSSPct, transcript.Integrity)
	if !res.Gate.Pass {
		t.Fatalf("F9 soak gate failed: %+v", res.Gate)
	}
}

// f9SoakRuntime is the live harness of one soak: one serve child plus the
// api-server child, all on the same data dir and fake provider.
type f9SoakRuntime struct {
	e          *Env
	p          *serveProc
	tg         *FakeTG
	api        *soakAPIServer
	webhookURL string
	// client is shared by the webhook, api-server and /metrics calls. A fresh
	// http.Client per metrics poll would leave one idle connection (and its
	// server goroutines) per sample, which the soak would report as a leak.
	client *http.Client
}

func startF9Soak(t *testing.T) *f9SoakRuntime {
	t.Helper()
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	// The non-owner identity needs an existing workspace: the gateway refuses
	// a webhook turn whose sandbox root does not exist (typed error, no crash).
	userCfg := fmt.Sprintf("[gateway.users.%q]\nworkspace = %q\n", soakWebhookID, e.Workdir)
	if err := os.WriteFile(e.cfg, []byte(userCfg), 0o600); err != nil {
		t.Fatalf("write gateway user config: %v", err)
	}
	seedSoakWebhookOwner(t, e.DataDir)
	env := append(gatewayServeEnv(e, tg),
		"DAMNBOT_GATEWAY_ALLOW="+soakWebhookID,
		"DAMNBOT_WEBHOOK_LISTEN=127.0.0.1:0",
		"DAMNBOT_WEBHOOK_SECRET="+genericWebhookSecret,
	)
	p := startServe(t, e, binPaths.main, env)
	p.awaitLog(p.stderr, "gateway: webhook listening on", 15*time.Second)
	rt := &f9SoakRuntime{e: e, p: p, tg: tg, webhookURL: soakWebhookURL(t, p), client: &http.Client{Timeout: 60 * time.Second}}
	rt.api = startSoakAPIServer(t, e)
	soakWaitReady(t, rt)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("serve stderr:\n%s", p.stderr.String())
			t.Logf("api-server stderr:\n%s", rt.api.stderr.String())
		}
	})
	return rt
}

// seedSoakWebhookOwner makes the second platform an owner identity. The daily
// USD guard caps role user identities and fails closed on a NULL-cost row; the
// fake provider has no price, so a role user webhook would be denied after its
// first turn. A real deployment can have owners on more than one platform, and
// EnsureIdentity never overwrites the pre-seeded owner row.
func seedSoakWebhookOwner(t *testing.T, dir string) {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatalf("open data dir for owner seed: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close owner-seed store: %v", cerr)
		}
	}()
	// Same row shape as gwstore.EnsureIdentity; the e2e import matrix does not
	// allow the store package to be imported from this package's tests.
	err = db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, display_name, role, status, strength, updated_at)
			VALUES (?1, ?2, ?2, 'owner', 'allowlisted', 'platform_authenticated', ?3)
			ON CONFLICT(platform, external_id) DO NOTHING`,
			"webhook", "e2e", time.Now().UnixMilli())
		return xerr
	})
	if err != nil {
		t.Fatalf("seed webhook owner: %v", err)
	}
}

// soakRunTurns drives every turn round-robin over the three surfaces and
// returns the post-warmup baseline and the quiet end sample.
func soakRunTurns(t *testing.T, rt *f9SoakRuntime, plan soakPlan) (start, end f9SoakSample, panicFree bool) {
	t.Helper()
	warmup := min(max(plan.turns/5, 2), 200, plan.turns)
	gatewayDone := 0
	for i := range plan.turns {
		turnStart := time.Now()
		gatewayDone = soakExecuteTurn(t, rt, i, plan.interval, gatewayDone)
		if i+1 == warmup {
			start = soakStableSample(t, rt)
		}
		soakPace(t, turnStart, plan.interval)
	}
	tgTurns, webhookTurns := soakSurfaceCounts(plan.turns)
	// Every gateway turn finishes before the process-level measurements, so a
	// still-running turn cannot be mistaken for a leak or a lost message.
	waitFor(t, 60*time.Second, "all gateway turns to finish", func() bool {
		ok, failed := soakTurns(t, rt.client, rt.p.addr)
		return ok+failed >= tgTurns+webhookTurns
	})
	settle(2 * time.Second)
	end = soakSampleQuiet(t, rt, start)
	rt.client.CloseIdleConnections()
	panicFree = !strings.Contains(rt.p.stderr.String(), "panic:") &&
		!strings.Contains(rt.p.stderr.String(), "fatal error:")
	return start, end, panicFree
}

// soakExecuteTurn sends one message on the surface selected by turn%3 and
// waits for its provider call (and, for Telegram, its final answer).
func soakExecuteTurn(t *testing.T, rt *f9SoakRuntime, turn int, interval time.Duration, gatewayDone int) int {
	t.Helper()
	surface := turn % 3
	marker := fmt.Sprintf("f9-soak-%d-%04d", surface, turn)
	answer := "soak-ok " + marker
	rt.e.Server.Push(providertest.Text(answer))
	want := turn + 1
	switch surface {
	case 0:
		rt.tg.PushText(gatewayOwner, marker)
		waitFor(t, soakTurnBudget(interval), "telegram turn "+marker, func() bool {
			return len(rt.e.Server.Requests()) == want
		})
		waitFor(t, soakTurnBudget(interval), "telegram answer "+marker, func() bool {
			return soakAnswerSeen(rt.tg, answer)
		})
	case 1:
		postGenericWebhook(t, rt.client, rt.webhookURL, genericWebhookSecret, soakWebhookEnvelope(t, marker, marker))
		waitFor(t, soakTurnBudget(interval), "webhook turn "+marker, func() bool {
			return len(rt.e.Server.Requests()) == want
		})
		waitFor(t, soakTurnBudget(interval), "webhook answer "+marker, func() bool {
			return soakAnswerSeen(rt.tg, answer)
		})
	default:
		soakAPITurn(t, rt, marker, answer)
		return gatewayDone
	}
	// Keep at most one gateway turn in flight: overlapping turns open extra
	// provider/Telegram connections and the acceptance numbers describe a
	// steady daemon, not a burst of parallel chats.
	next := gatewayDone + 1
	waitFor(t, soakTurnBudget(interval), "gateway turn completion", func() bool {
		ok, failed := soakTurns(t, rt.client, rt.p.addr)
		return ok+failed >= next
	})
	return next
}

// soakAnswerSeen reports whether one final answer reached the fake bot chat.
func soakAnswerSeen(tg *FakeTG, answer string) bool {
	edits := tg.Edits()
	for i := range edits {
		if edits[i].Text == answer {
			return true
		}
	}
	return false
}

// soakSurfaceCounts returns how many turns each gateway surface receives for
// the fixed round-robin order.
func soakSurfaceCounts(turns int) (telegram, webhook int) {
	for i := range turns {
		switch i % 3 {
		case 0:
			telegram++
		case 1:
			webhook++
		}
	}
	return telegram, webhook
}

// soakVerifyTranscript opens the stopped data dir read-only and counts the
// exactly-once evidence plus PRAGMA integrity_check.
func soakVerifyTranscript(t *testing.T, dir string, turns int) f9SoakTranscript {
	t.Helper()
	db := openChaosDB(t, dir)
	defer db.close()
	transcript := soakTranscript(t, db, turns)
	if got := db.integrity("F9 soak"); got != "ok" {
		t.Errorf("integrity_check after %d turns = %s", turns, got)
	} else {
		transcript.Integrity = got
	}
	return transcript
}

// f9SoakResult assembles the artefact and evaluates the acceptance gate.
func f9SoakResult(rt *f9SoakRuntime, plan soakPlan, profile string, started time.Time,
	start, end f9SoakSample, panicFree bool, transcript *f9SoakTranscript,
) f9Soak {
	tgTurns, webhookTurns := soakSurfaceCounts(plan.turns)
	transcript.Placeholders = len(rt.tg.Sends())
	transcript.TelegramDone = soakFinalAnswers(rt.tg, 0)
	transcript.WebhookDone = soakFinalAnswers(rt.tg, 1)
	res := f9Soak{
		Meta: f9SoakMeta{
			Phase: "F9", Test: "TestF9Soak24h", Profile: profile, Mode: soakMode(plan),
			Date: time.Now().UTC().Format(time.RFC3339), DurationSec: time.Since(started).Seconds(),
			Turns: plan.turns, IntervalMS: float64(plan.interval.Microseconds()) / 1000,
			Platforms: 3, ProviderReqs: len(rt.e.Server.Requests()), Commit: soakCommit(),
		},
		Surfaces: []string{"telegram", "webhook", "api_server"},
		Start:    start, End: end,
		Deltas: f9SoakDeltas{
			Goroutines: end.Goroutines - start.Goroutines,
			FDs:        end.FDs - start.FDs,
			RSSPct:     soakRSSDeltaPct(start.RSSKB, end.RSSKB),
		},
		Transcript: *transcript,
	}
	res.Gate = f9SoakGate{
		Goroutines: res.Deltas.Goroutines <= 0,
		FDs:        res.Deltas.FDs <= soakFDSlack,
		RSS:        res.Deltas.RSSPct < soakRSSGrowthPct,
		Messages: transcript.UserMessages == plan.turns && transcript.TurnStarts == plan.turns &&
			transcript.TurnEnds == plan.turns && transcript.Crashes == 0 &&
			transcript.Placeholders == tgTurns+webhookTurns &&
			transcript.TelegramDone == tgTurns && transcript.WebhookDone == webhookTurns &&
			len(transcript.Duplicates) == 0 && len(transcript.Missing) == 0,
		Integrity: transcript.Integrity == "ok",
		NoPanic:   panicFree,
	}
	res.Gate.Pass = res.Gate.Goroutines && res.Gate.FDs && res.Gate.RSS && res.Gate.Messages &&
		res.Gate.Integrity && res.Gate.NoPanic
	return res
}

// soakWaitReady waits for /readyz, so the baseline is taken from a daemon that
// has finished opening adapters, DB and the observability listener.
func soakWaitReady(t *testing.T, rt *f9SoakRuntime) {
	t.Helper()
	waitFor(t, 30*time.Second, "serve /readyz", func() bool {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rt.p.url("/readyz"), nil)
		if err != nil {
			return false
		}
		resp, err := rt.client.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }() //nolint:errcheck // readiness only needs the status
		if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
			return false
		}
		return resp.StatusCode == http.StatusOK
	})
}

// soakWebhookURL extracts the ephemeral generic-webhook address from stderr.
func soakWebhookURL(t *testing.T, p *serveProc) string {
	t.Helper()
	m := genericWebhookAddrRE.FindStringSubmatch(p.stderr.String())
	if m == nil {
		t.Fatalf("generic webhook address missing from serve stderr:\n%s", p.stderr.String())
	}
	return "http://" + m[1] + "/webhook"
}

// soakAPIServer is the third fake platform: the real api-server child on the
// same data dir, so the soak also exercises the P19 multi-process contract.
type soakAPIServer struct {
	cmd     *exec.Cmd
	addr    string
	stderr  *apiSyncBuffer
	stopped bool
}

func startSoakAPIServer(t *testing.T, e *Env) *soakAPIServer {
	t.Helper()
	args := []string{
		"api-server", "--addr", "127.0.0.1:0", "--model", modelRef,
		"--base-url", e.Server.URL, "--api-key", "e2e", "--offline",
		"--data-dir", e.DataDir, "--config", e.cfg,
	}
	cmd := exec.CommandContext(context.Background(), binPaths.main, args...) // #nosec G204 -- this package's temp build
	cmd.Dir = e.Workdir
	cmd.Env = append(e.childEnv(), "DAMNBOT_API_TOKEN="+soakAPIKey)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr apiSyncBuffer
	cmd.Stderr = &stderr
	if serr := cmd.Start(); serr != nil {
		t.Fatalf("start api-server: %v", serr)
	}
	addr, lerr := waitListen(t, pipe, &stdout, &stderr)
	if lerr != nil {
		t.Fatalf("api-server listen: %v", lerr)
	}
	api := &soakAPIServer{cmd: cmd, addr: addr, stderr: &stderr}
	t.Cleanup(func() { api.stop(t) })
	return api
}

// stop is the clean path (SIGTERM → exit 0). A non-zero exit is a crash.
func (a *soakAPIServer) stop(t *testing.T) {
	t.Helper()
	if a == nil || a.cmd == nil || a.cmd.Process == nil || a.stopped {
		return
	}
	a.stopped = true
	if err := a.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Logf("api-server interrupt: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- a.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("api-server exited with %v, want 0 (clean shutdown)\nstderr:\n%s", err, a.stderr.String())
		}
	case <-time.After(15 * time.Second):
		if kerr := a.cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			t.Logf("api-server kill: %v", kerr)
		}
		if werr := <-done; werr != nil {
			t.Logf("api-server wait after kill: %v", werr)
		}
		t.Errorf("api-server did not stop within 15s\nstderr:\n%s", a.stderr.String())
	}
}

// soakAPITurn sends one OpenAI-shaped prompt and checks the answer envelope.
func soakAPITurn(t *testing.T, rt *f9SoakRuntime, marker, answer string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":    modelRef,
		"messages": []map[string]string{{"role": "user", "content": marker}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+rt.api.addr+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+soakAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := rt.client.Do(req)
	if err != nil {
		t.Fatalf("api-server turn %s: %v", marker, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // body fully read below
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("api-server read %s: %v", marker, rerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("api-server turn %s status = %d body = %s", marker, resp.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("api-server decode %s: %v (%s)", marker, err, raw)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != answer {
		t.Fatalf("api-server turn %s answer = %s, want %q", marker, raw, answer)
	}
}

// soakPace keeps the requested turn interval; the turn itself runs in the
// background between ticks.
func soakPace(t *testing.T, started time.Time, interval time.Duration) {
	t.Helper()
	if wait := interval - time.Since(started); wait > 0 {
		<-time.After(wait)
	}
}

// soakTurnBudget bounds one turn: the regular interval plus recovery slack.
func soakTurnBudget(interval time.Duration) time.Duration {
	if interval*3 > 30*time.Second {
		return interval * 3
	}
	return 30 * time.Second
}

func soakProfile(plan soakPlan) string {
	switch {
	case plan.hours == soakAcceptanceHours && plan.turns == soakAcceptanceTurns:
		return "acceptance-24h-1000t"
	case plan.hours > 0:
		return fmt.Sprintf("%gh-%dt", plan.hours, plan.turns)
	default:
		return fmt.Sprintf("ci-%s-%dt", plan.window, plan.turns)
	}
}

func soakMode(plan soakPlan) string {
	switch {
	case plan.hours >= soakAcceptanceHours:
		return "24h"
	case plan.hours > 0:
		return "hours"
	default:
		return "ci"
	}
}

func soakSampleNow(t *testing.T, rt *f9SoakRuntime) f9SoakSample {
	t.Helper()
	proc, err := sampleProc(rt.p.cmd.Process.Pid)
	if err != nil {
		t.Fatalf("sample serve: %v", err)
	}
	fds, err := processFDs(rt.p.cmd.Process.Pid)
	if err != nil {
		t.Fatalf("count serve fds: %v", err)
	}
	body := soakMetricsBody(t, rt.client, rt.p.addr)
	return f9SoakSample{RSSKB: proc.rssKB, Goroutines: soakMetricInt(t, body, "damnbot_goroutines"), FDs: fds}
}

// soakSampleQuiet waits up to 30 s for the accept-loop and turn workers to be
// reaped: sampling the instant the last turn ends would report pooled HTTP and
// finishing workers as a leak. A real leak never reaches the baseline and the
// gate fails on the sample that is returned.
// soakStableSample drops the harness's own keep-alive connections before it
// reads the process: the soak client talks to serve's webhook and metrics
// listeners, and those idle sockets (with their two serve-side goroutines) are
// harness overhead, not a daemon leak. The metrics request itself is counted on
// both sides of the comparison, so the delta stays meaningful.
func soakStableSample(t *testing.T, rt *f9SoakRuntime) f9SoakSample {
	t.Helper()
	rt.client.CloseIdleConnections()
	<-time.After(150 * time.Millisecond)
	s := soakSampleNow(t, rt)
	rt.client.CloseIdleConnections()
	return s
}

func soakSampleQuiet(t *testing.T, rt *f9SoakRuntime, baseline f9SoakSample) f9SoakSample {
	t.Helper()
	deadline := time.Now().Add(soakIdleSettle)
	for {
		now := soakStableSample(t, rt)
		if now.Goroutines <= baseline.Goroutines && now.FDs <= baseline.FDs+soakFDSlack {
			return now
		}
		if time.Now().After(deadline) {
			return now
		}
		<-time.After(500 * time.Millisecond)
	}
}

func processFDs(pid int) (int, error) {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0, fmt.Errorf("read /proc/%d/fd: %w", pid, err)
	}
	return len(entries), nil
}

func soakRSSDeltaPct(startKB, endKB int) float64 {
	if startKB <= 0 {
		return 0
	}
	return float64(endKB-startKB) / float64(startKB) * 100
}

// soakMetricsBody fetches /metrics over the shared client.
func soakMetricsBody(t *testing.T, client *http.Client, addr string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("metrics close: %v", cerr)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("metrics read: %v", err)
	}
	return string(body)
}

// soakMetricInt reads one unlabelled integer sample from the exposition.
func soakMetricInt(t *testing.T, body, name string) int {
	t.Helper()
	for line := range strings.SplitSeq(body, "\n") {
		rest, found := strings.CutPrefix(line, name+" ")
		if !found {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			t.Fatalf("metric %q: %v", line, err)
		}
		return n
	}
	t.Fatalf("%s missing from /metrics", name)
	return 0
}

// soakTurns reads the completed-turn counters from /metrics.
func soakTurns(t *testing.T, client *http.Client, addr string) (ok, failed int) {
	t.Helper()
	body := soakMetricsBody(t, client, addr)
	for line := range strings.SplitSeq(body, "\n") {
		if rest, found := strings.CutPrefix(line, `damnbot_turns_total{result="ok"} `); found {
			ok = atoiMetric(t, line, rest)
		}
		if rest, found := strings.CutPrefix(line, `damnbot_turns_total{result="error"} `); found {
			failed = atoiMetric(t, line, rest)
		}
	}
	return ok, failed
}

func atoiMetric(t *testing.T, line, raw string) int {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		t.Fatalf("metric %q: %v", line, err)
	}
	return n
}

// soakTranscript verifies exactly one user_message per submitted marker, one
// turn_start and one turn_end for its turn, and no crash_recovered event.
func soakTranscript(t *testing.T, db *chaosDB, turns int) f9SoakTranscript {
	t.Helper()
	counts := map[string]int{}
	tr := f9SoakTranscript{}
	events := db.events()
	for i := range events {
		ev := &events[i]
		switch ev.Kind {
		case "user_message":
			if strings.HasPrefix(ev.Content, "f9-soak-") {
				counts[ev.Content]++
				tr.UserMessages++
			}
		case "turn_start":
			tr.TurnStarts++
		case "turn_end":
			tr.TurnEnds++
		case "crash_recovered":
			tr.Crashes++
		}
	}
	for i := range turns {
		marker := fmt.Sprintf("f9-soak-%d-%04d", i%3, i)
		switch counts[marker] {
		case 1:
		case 0:
			tr.Missing = append(tr.Missing, marker)
		default:
			tr.Duplicates = append(tr.Duplicates, marker)
		}
	}
	tr.Integrity = "not checked"
	return tr
}

// soakWebhookEnvelope is the generic webhook delivery of the third surface.
// The chat id is the fake bot's own chat: the gateway has exactly one outbound
// adapter (Telegram), so a webhook turn finalises by replying there. The
// marker prefix distinguishes the two surfaces in the outbound transcript.
func soakWebhookEnvelope(t *testing.T, id, text string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": id, "event": "message.created", "chat": strconv.FormatInt(gatewayOwner, 10),
		"sender": "e2e", "text": text, "mentioned": true,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}

// soakFinalAnswers counts successful final edits per surface prefix; a
// duplicate would push a count above that surface's turn count.
func soakFinalAnswers(tg *FakeTG, surface int) int {
	prefix := fmt.Sprintf("soak-ok f9-soak-%d-", surface)
	n := 0
	edits := tg.Edits()
	for i := range edits {
		if strings.HasPrefix(edits[i].Text, prefix) {
			n++
		}
	}
	return n
}

// writeF9Artifact stores the compact JSON log under bench/f9/results/.
func writeF9Artifact(t *testing.T, kind, profile string, v any) {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("artifact root: %v", err)
	}
	dir := filepath.Join(root, "bench", "f9", "results")
	if xerr := os.MkdirAll(dir, 0o750); xerr != nil {
		t.Fatalf("artifact dir: %v", xerr)
	}
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatalf("artifact marshal: %v", err)
	}
	b = append(b, '\n')
	out := filepath.Join(dir, fmt.Sprintf("%s-%s-%s-F9.json", kind, sanitizeProfile(profile), soakCommit()))
	if werr := os.WriteFile(out, b, 0o600); werr != nil {
		t.Fatalf("artifact write: %v", werr)
	}
	t.Logf("artifact: %s", out)
}

func sanitizeProfile(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, strings.ToLower(s))
}

func soakCommit() string {
	out, err := exec.CommandContext(context.Background(), "git", "rev-parse", "--short", "HEAD").Output() // #nosec G204 -- fixed git command
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// TestF9ChaosFull is the full chaos acceptance leg. The kill count is 50 in CI
// and 1.000 with DAMNBOT_CHAOS_KILLS=1000 (`make chaos-full`). The 50-client
// window delegates to the canonical ctxdb test, so there is exactly one
// implementation of the GAP-REL-03 evidence.
func TestF9ChaosFull(t *testing.T) {
	kills := f9ChaosKills(t)
	started := time.Now()
	t.Run("Kill9", func(t *testing.T) {
		chaosKill9Rounds(t, kills)
	})
	t.Run("NetworkCut", chaosNetworkCut)
	t.Run("DiskFullTmpfs", chaosDiskFullTmpfs)
	t.Run("CorruptConfig", chaosCorruptConfig)
	t.Run("CorruptDB", chaosCorruptDB)
	t.Run("Reboot", chaosReboot)
	t.Run("RestartGateway", chaosRestartGateway)
	t.Run("Clients50", f9Chaos50Clients)
	res := map[string]any{
		"phase": "F9", "test": "TestF9ChaosFull", "kills": kills,
		"scenarios":        []string{"Kill9", "NetworkCut", "DiskFullTmpfs", "CorruptConfig", "CorruptDB", "Reboot", "RestartGateway", "Clients50"},
		"duration_seconds": time.Since(started).Seconds(),
		"commit":           soakCommit(),
		"pass":             !t.Failed(),
	}
	writeF9Artifact(t, "chaos", fmt.Sprintf("%dkills", kills), res)
}

// f9ChaosKills resolves the kill -9 round count: 50 by default for CI, 1.000
// for the acceptance run.
func f9ChaosKills(t *testing.T) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("DAMNBOT_CHAOS_KILLS"))
	if raw == "" {
		return 50
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("DAMNBOT_CHAOS_KILLS=%q is not a positive integer", raw)
	}
	return n
}

// chaosDiskFullTmpfs uses a real 2 MiB tmpfs when the runner may mount one
// (root/CAP_SYS_ADMIN, e.g. a dedicated acceptance host). Unprivileged runners
// fall back to chaosDiskFull, which injects SQLITE_FULL — the exact driver code
// a full volume produces (ENOSPC/EDQUOT) — so the error path is still tested.
//
//nolint:gocognit,gocyclo // one scenario: mount, fill, assert the typed error, recover
func chaosDiskFullTmpfs(t *testing.T) {
	dir := t.TempDir()
	if merr := unix.Mount("tmpfs", dir, "tmpfs", 0, "size=2m,nosuid,nodev"); merr != nil {
		t.Logf("tmpfs mount unavailable (%v); falling back to SQLITE_FULL injection", merr)
		if rerr := os.Remove(dir); rerr != nil {
			t.Logf("remove temp dir: %v", rerr)
		}
		chaosDiskFull(t)
		return
	}
	defer func() {
		if uerr := unix.Unmount(dir, 0); uerr != nil {
			t.Errorf("unmount tmpfs: %v", uerr)
		}
		if rerr := os.Remove(dir); rerr != nil {
			t.Logf("remove tmpfs mountpoint: %v", rerr)
		}
	}()
	dataDir := filepath.Join(dir, "data")
	s, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dataDir, Kind: "serve", BusyTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("open tmpfs store: %v", err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("close tmpfs store: %v", cerr)
		}
	}()
	const sid = "f9-tmpfs"
	if _, cerr := s.CreateSession(t.Context(), ctxdb.Session{ID: sid}); cerr != nil {
		t.Fatalf("create session on tmpfs: %v", cerr)
	}
	large := strings.Repeat("tmpfs-full-payload-", 4096)
	if _, aerr := s.Append(t.Context(), sid, nil, ctxdb.Event{Kind: "user_message", Content: large}); aerr != nil {
		t.Fatalf("control append on tmpfs: %v", aerr)
	}
	filler := filepath.Join(dir, "filler")
	fh, ferr := os.Create(filler) // #nosec G304 -- the test's own tmpfs mount
	if ferr != nil {
		t.Fatalf("create filler: %v", ferr)
	}
	chunk := make([]byte, 64*1024)
	var full error
	for range 64 {
		if _, werr := fh.Write(chunk); werr != nil {
			full = werr
			break
		}
	}
	if cerr := fh.Close(); cerr != nil {
		t.Fatalf("close filler: %v", cerr)
	}
	if full == nil {
		t.Fatal("tmpfs did not report ENOSPC after 4 MiB of filler")
	}
	var diskErr error
	for range 64 {
		if _, aerr := s.Append(t.Context(), sid, nil, ctxdb.Event{Kind: "user_message", Content: large}); aerr != nil {
			diskErr = aerr
			break
		}
	}
	if diskErr == nil || !strings.Contains(diskErr.Error(), "disk_full") {
		t.Fatalf("append on full tmpfs = %v, want the typed disk_full error", diskErr)
	}
	if res, ierr := s.IntegrityCheck(t.Context()); ierr != nil || res != "ok" {
		t.Fatalf("integrity on full tmpfs = %q, %v", res, ierr)
	}
	if rerr := os.Remove(filler); rerr != nil {
		t.Fatalf("free tmpfs space: %v", rerr)
	}
	if _, aerr := s.Append(t.Context(), sid, nil, ctxdb.Event{Kind: "user_message", Content: "after tmpfs full"}); aerr != nil {
		t.Fatalf("append after freeing tmpfs: %v", aerr)
	}
}

// chaosRestartGateway is the clean restart leg: SIGTERM exits 0, the same data
// dir starts again, the persisted conversation continues and a clean stop adds
// no crash_recovered marker.
func chaosRestartGateway(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	e.Server.Push(providertest.Text("before restart"), providertest.Text("after restart"))
	p := startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "before restart")
	waitFor(t, 20*time.Second, "the pre-restart answer", func() bool {
		return soakAnswerSeen(tg, "before restart")
	})
	gatewayStop(t, p)
	if strings.Contains(p.stderr.String(), "panic:") {
		t.Fatalf("serve panicked before restart:\n%s", p.stderr.String())
	}
	p = startGatewayServe(t, e, tg)
	tg.PushText(gatewayOwner, "after restart")
	waitFor(t, 20*time.Second, "the post-restart answer", func() bool {
		return soakAnswerSeen(tg, "after restart")
	})
	if strings.Contains(p.stderr.String(), "panic:") {
		t.Fatalf("serve panicked after restart:\n%s", p.stderr.String())
	}
	gatewayStop(t, p)
	ro := openChaosDB(t, e.DataDir)
	defer ro.close()
	if n := ro.count("crash_recovered", nil); n != 0 {
		t.Fatalf("clean restart added %d crash_recovered rows, want 0", n)
	}
}

// f9Chaos50Clients runs the canonical GAP-REL-03 test in its own process. The
// F9 suite must not grow a second 50-client implementation: the evidence is
// the ctxdb test, named exactly as the acceptance ledger names it.
func f9Chaos50Clients(t *testing.T) {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_AS, &limit); err != nil {
		t.Fatalf("read RLIMIT_AS: %v", err)
	}
	if limit.Cur > 0 && limit.Cur < 8<<30 {
		// Fifty client processes each start a Go runtime; the 4 GiB address
		// cap of the shared-host wrapper makes pthread_create fail (EAGAIN),
		// which is an environment limit, not a P19 result. `make
		// chaos-nightly` runs the same canonical test without that cap.
		t.Skipf("50-client window needs >8 GiB address space (RLIMIT_AS=%d)", limit.Cur)
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("module root: %v", err)
	}
	dur := strings.TrimSpace(os.Getenv("DAMNBOT_CHAOS_DURATION"))
	if dur == "" {
		dur = "30s"
	}
	cmd := exec.CommandContext(t.Context(), "go", "test", "-tags", "goolm", "-count=1",
		"-timeout", "10m", "-run", "^TestChaos50Clients$", "./internal/ctxdb") // #nosec G204 -- fixed toolchain and package
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "DAMNBOT_CHAOS_DURATION="+dur, "GOMAXPROCS=2")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("TestChaos50Clients (window %s): %v\n%s", dur, err, out)
	}
}
