package tools

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/redact"
)

// paymentFixture is a shell tool whose shell_allow matches exactly one
// command naming a script of a payment skill inside the workspace (so the
// match alone would run headless).
type paymentFixture struct {
	t      *testing.T
	w      *workspace
	payDir string
	call   Call
}

func newPaymentFixture(t *testing.T) *paymentFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	w := newWorkspace(t)
	f := &paymentFixture{t: t, w: w, payDir: w.root + "/.skills/stripe-pay"}
	args, err := json.Marshal(map[string]string{"command": "echo " + f.payDir + "/scripts/charge.sh"})
	if err != nil {
		t.Fatal(err)
	}
	f.call = Call{TurnID: "t1", CallID: "c-pay", Name: "shell", Args: args}
	return f
}

func (f *paymentFixture) shell(runner *fakeRunner, monetary []string) Tool {
	id := classify.BinaryID{Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}
	return mustShell(f.t, &ShellConfig{
		Runner: runner, Spill: newMemSpill(), Redactor: redact.New(redact.Options{}),
		Roots: []string{f.w.root}, Home: f.w.root, Cwd: f.w.root,
		ParentEnv: os.Environ(), EnvPolicy: testEnvPolicy(),
		Allow:            []classify.AllowEntry{{"echo", f.payDir + "/scripts/charge.sh"}},
		SessionStart:     map[string]classify.BinaryID{"echo": id},
		Resolve:          func(string) (classify.BinaryID, bool) { return id, true },
		WorkspaceTrusted: true, MonetaryPaths: monetary, Canonicalize: testPathKey,
	})
}

// TestShellPaymentSkillGate: a shell command touching a skill that declares
// side_effects: [payment] takes the Monetary approval path (SEC-15,
// ADR-133): in mode auto it prompts with a preview despite the shell_allow
// match, never offers allow_session, and a session answer does not stick.
func TestShellPaymentSkillGate(t *testing.T) {
	t.Parallel()
	f := newPaymentFixture(t)
	runner := &fakeRunner{}
	appr := &approverRec{answer: ApprovalAllowSession}
	d, _, _ := f.w.dispatcher(t, ModeAuto, appr, f.shell(runner, []string{f.payDir}))
	for i := range 2 {
		call := f.call
		call.CallID += strconv.Itoa(i)
		if out := d.Dispatch(context.Background(), call); out.Err != nil {
			t.Fatalf("call %d: %s", i, out.Render())
		}
	}
	reqs := appr.requests()
	if len(reqs) != 2 || runner.calls != 2 {
		t.Fatalf("prompts = %d, runs = %d: every payment call must ask", len(reqs), runner.calls)
	}
	for _, r := range reqs {
		if r.AllowSession || !strings.Contains(r.Class, "monetary") || !strings.Contains(r.Preview, "payment skill file") {
			t.Fatalf("request = %+v", r)
		}
	}
}

// TestShellUnsafeAutoShellPayment: `cd <payment skill>; ./script` never runs
// under run --approval auto --unsafe-auto-shell (SEC10-1); the first case is
// Monetary, a non-payment cd leaves a relative argv0 Unknown — both deny.
func TestShellUnsafeAutoShellPayment(t *testing.T) {
	t.Parallel()
	f := newPaymentFixture(t)
	id := classify.BinaryID{Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}
	shop := mustShell(t, &ShellConfig{
		Runner: &fakeRunner{}, Spill: newMemSpill(), Redactor: redact.New(redact.Options{}),
		Roots: []string{f.w.root}, Home: f.w.root, Cwd: f.w.root,
		ParentEnv: os.Environ(), EnvPolicy: testEnvPolicy(),
		SessionStart:     map[string]classify.BinaryID{"echo": id},
		Resolve:          func(string) (classify.BinaryID, bool) { return id, true },
		WorkspaceTrusted: true, UnsafeAutoShell: true, MonetaryPaths: []string{f.payDir}, Canonicalize: testPathKey,
	})
	d, _, _ := f.w.dispatcher(t, ModeHeadlessAuto, nil, shop)
	for i, cmd := range []string{
		"cd " + f.payDir + "/scripts; ./charge.sh --amount 100",
		"cd " + f.payDir + " && ./scripts/charge.sh",
	} {
		if out := d.Dispatch(context.Background(), Call{TurnID: "t1", CallID: "u" + strconv.Itoa(i), Name: "shell", Args: mustArgs(t, cmd)}); codeOfOutcome(out) != CodeDenied {
			t.Fatalf("%q: %s, want denied", cmd, out.Render())
		}
	}
}

// mustArgs encodes one shell command argument.
func mustArgs(t *testing.T, command string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestShellPaymentSkillHeadless: the same exact shell_allow match runs
// headless without the declaration, and is denied unexecuted with it, also
// under --approval auto.
func TestShellPaymentSkillHeadless(t *testing.T) {
	t.Parallel()
	f := newPaymentFixture(t)
	control := &fakeRunner{}
	d, _, _ := f.w.dispatcher(t, ModeHeadless, nil, f.shell(control, nil))
	if out := d.Dispatch(context.Background(), f.call); out.Err != nil || control.calls != 1 {
		t.Fatalf("control: %s, runs %d", out.Render(), control.calls)
	}
	for _, mode := range []Mode{ModeHeadless, ModeHeadlessAuto} {
		runner := &fakeRunner{}
		appr := &approverRec{answer: ApprovalAllowOnce}
		d, _, _ := f.w.dispatcher(t, mode, appr, f.shell(runner, []string{f.payDir}))
		out := d.Dispatch(context.Background(), f.call)
		if codeOfOutcome(out) != CodeDenied || runner.calls != 0 || len(appr.requests()) != 0 {
			t.Fatalf("mode %d: code %s, runs %d, prompts %d", mode, codeOfOutcome(out), runner.calls, len(appr.requests()))
		}
	}
}

// TestMonetaryAlwaysPrompts is the GAP-SEC-15 acceptance corpus: 15
// monetary commands from all three triggers — classifier argv rules,
// mutating HTTP to security.monetary_hosts, and payment-skill files — each
// prompt in mode auto (allow_session never offered or honoured; the
// smart_approval key has no effect, ADR-079 butir 9) and are denied
// unexecuted headless.
func TestMonetaryAlwaysPrompts(t *testing.T) {
	t.Parallel()
	f := newPaymentFixture(t)
	pay := f.payDir + "/scripts/"
	corpus := []string{
		"link-cli spend-request create --amount 10",
		"stripe charges create --amount 100 --currency usd",
		"stripe payment_intents create --amount 100",
		"stripe transfers create --amount 100 --destination acct_1",
		"stripe payouts create --amount 100",
		"solana transfer 7xKX 1",
		"cast send 0xabc --value 1ether",
		"gh sponsor octocat",
		"sh -c 'stripe payouts create --amount 5'",
		"curl -X POST https://api.stripe.com/v1/charges -d amount=100",
		"wget --post-data amount=1 https://api.stripe.com/v1/payouts",
		"python3 " + pay + "charge.py --amount 100",
		pay + "charge.sh --amount 100",
		"bash " + pay + "refund.sh",
		"node " + pay + "transfer.js",
		"cd " + f.payDir + "/scripts; ./charge.sh --amount 100",
		"curl -H 'Host: api.stripe.com' https://1.2.3.4/v1/charges",
	}
	shell := func(runner *fakeRunner) Tool {
		return mustShell(t, &ShellConfig{
			Runner: runner, Spill: newMemSpill(), Redactor: redact.New(redact.Options{}),
			Roots: []string{f.w.root}, Home: f.w.root, Cwd: f.w.root,
			ParentEnv: os.Environ(), EnvPolicy: testEnvPolicy(), WorkspaceTrusted: true,
			MonetaryHosts: []string{"api.stripe.com"}, MonetaryPaths: []string{f.payDir}, Canonicalize: testPathKey,
		})
	}
	for _, mode := range []Mode{ModeAuto, ModeHeadlessAuto} {
		runMonetaryMode(t, f, corpus, mode, shell)
	}
}

// runMonetaryMode asserts one mode over the monetary corpus: auto prompts
// for each command with AllowSession=false; headless denies and runs none.
func runMonetaryMode(t *testing.T, f *paymentFixture, corpus []string, mode Mode, shell func(*fakeRunner) Tool) {
	t.Helper()
	runner := &fakeRunner{}
	appr := &approverRec{answer: ApprovalAllowSession}
	d, _, _ := f.w.dispatcher(t, mode, appr, shell(runner))
	for i, cmd := range corpus {
		args, err := json.Marshal(map[string]string{"command": cmd})
		if err != nil {
			t.Fatal(err)
		}
		out := d.Dispatch(context.Background(), Call{TurnID: "t1", CallID: "m" + strconv.Itoa(i), Name: "shell", Args: args})
		if mode.Headless() != (codeOfOutcome(out) == CodeDenied) {
			t.Errorf("mode %d %q: %s", mode, cmd, out.Render())
		}
	}
	reqs := appr.requests()
	if mode.Headless() {
		if len(reqs) != 0 || runner.calls != 0 {
			t.Fatalf("headless: prompts %d, runs %d", len(reqs), runner.calls)
		}
		return
	}
	if len(reqs) != len(corpus) || runner.calls != len(corpus) {
		t.Fatalf("auto: prompts %d, runs %d, want %d", len(reqs), runner.calls, len(corpus))
	}
	for i, r := range reqs {
		if r.AllowSession || !strings.Contains(r.Class, "monetary") {
			t.Errorf("%q: request %+v", corpus[i], r)
		}
	}
}

// TestNewShellNeedsCanonicalize: a monetary gate without a canonicalizer is
// refused, so the bypassable lexical fallback cannot be used (SEC10-17).
func TestNewShellNeedsCanonicalize(t *testing.T) {
	t.Parallel()
	if _, err := NewShell(&ShellConfig{MonetaryPaths: []string{"/x"}}); err == nil {
		t.Fatal("MonetaryPaths without Canonicalize accepted")
	}
	if _, err := NewShell(nil); err == nil {
		t.Fatal("nil config accepted")
	}
}
