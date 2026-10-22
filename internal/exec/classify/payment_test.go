package classify

import (
	"path/filepath"
	"strings"
	"testing"
)

const testPaySkill = "/home/u/.config/damnbot/skills/stripe-pay"

// TestPaymentSkillMonetary: a command that runs or names a file of a skill
// declaring side_effects: [payment] is Monetary (SEC-15, ADR-133) — direct
// execution, interpreter scripts, operands and symlinks into the skill — and
// Monetary is never auto-approved: not by shell_allow, not headless, not by
// --unsafe-auto-shell.
func TestPaymentSkillMonetary(t *testing.T) {
	t.Parallel()
	env := testEnv()
	env.MonetaryPaths = []string{testPaySkill + "/"}
	env.Resolver = fakeFS{links: map[string]string{
		testRoot + "/pay.sh":              testPaySkill + "/scripts/charge.sh",
		testPaySkill + "/scripts/link.py": "/tmp/outside.py",
	}}
	cases := []struct {
		cmd  string
		want Class
	}{
		{testPaySkill + "/scripts/charge.sh --amount 100", Monetary},
		{"python3 " + testPaySkill + "/scripts/charge.py --amount 100", Monetary},
		{"bash " + testPaySkill + "/run.sh", Monetary},
		{"cat " + testPaySkill + "/scripts/link.py", Monetary}, // symlink target outside (SEC10-12)
		{"cd /tmp && node " + testPaySkill + "/scripts/pay.js", Monetary},
		{"./pay.sh", Monetary}, // symlink inside the workspace resolves into the skill
		{"sh -c 'python3 " + testPaySkill + "/scripts/x.py'", Monetary},
		{"cd " + testPaySkill + "/scripts; ./charge.sh --amount 100", Monetary},
		{"cd " + testPaySkill + " && ./scripts/charge.sh", Monetary},
		{"cd /tmp && ./charge.sh", Unknown},                                     // relative argv0 after cd: never auto
		{"curl https://api.stripe.com/v1/charges", Monetary},                    // GET (SEC10-7)
		{"curl -H 'Host: api.stripe.com' https://1.2.3.4/v1/charges", Monetary}, // Host header
		{"curl -X POST https://api.stripe.com/v1/charges", Monetary},
		{"python3 " + testPaySkill + "-v2/scripts/x.py", Mutating}, // sibling prefix, not the skill
		{"python3 /home/u/.config/damnbot/skills/other/x.py", Mutating},
		{"python3 tools/charge.py", Mutating},
	}
	for _, c := range cases {
		v := Classify(c.cmd, env)
		if v.Class != c.want {
			t.Errorf("%q: class %s, want %s (%v)", c.cmd, v.Class, c.want, v.Reasons)
			continue
		}
		if c.want != Monetary {
			continue
		}
		got := make([]byte, 0, 5)
		for _, m := range modes() {
			ctx := Context{Mode: m, AllowMatched: true, WorkspaceTrusted: true, CwdInRoot: true}
			got = append(got, decisionCode(Decide(&v, &ctx)))
		}
		if string(got) != "PPPDD" {
			t.Errorf("%q: decisions %s, want PPPDD (ask+preview everywhere, headless denied)", c.cmd, got)
		}
	}
	env.MonetaryPaths = nil
	if v := Classify(testPaySkill+"/scripts/charge.sh", env); v.Class == Monetary {
		t.Error("no payment skill configured: Monetary must come only from declarations or rules")
	}
}

// TestPaymentKeySimulatedForms: the gate compares canonical keys, so a short
// name and a differently-cased spelling of the same directory are Monetary
// (Windows 8.3 / case-insensitive APFS). The keyer is injected here because
// those forms are only constructible on their own OS.
func TestPaymentKeySimulatedForms(t *testing.T) {
	t.Parallel()
	key := func(p string) string {
		k := strings.ToLower(filepath.ToSlash(p))
		return strings.ReplaceAll(k, "runner~1", "runneradmin")
	}
	env := testEnv()
	env.MonetaryPaths = []string{"/tmp/RunnerAdmin/Stripe-Pay/"}
	env.MonetaryKey = key
	for _, cmd := range []string{
		"/tmp/runner~1/stripe-pay/scripts/charge.sh --amount 1",
		"python3 /tmp/RUNNER~1/STRIPE-PAY/scripts/charge.py",
	} {
		v := Classify(cmd, env)
		if v.Class != Monetary {
			t.Errorf("%q: class %s, want monetary", cmd, v.Class)
		}
	}
	if v := Classify("/tmp/runner~1/stripe-pay-v2/scripts/x.py", env); v.Class == Monetary {
		t.Error("sibling directory prefix matched")
	}
}
