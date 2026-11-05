//go:build !minimal

package browser

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// injectionCanary is a value that only appears inside a refused parameter. It
// must never surface in an approval preview, an error, or a result.
const injectionCanary = "GAPSEC05-CANARY-session-cookie"

// injectionCase is one attempt to smuggle a call past the reviewed policy.
// approval is the number of approval requests the attempt may raise before the
// refusal: 0 for structural refusals, 1 for a sensitive family refused by the
// approval seam.
type injectionCase struct {
	name     string
	args     string
	answer   host.Approval
	approval int
}

// TestF6GAPSEC05InjectionCorpus (GAP-SEC-05): twenty injection variants never
// reach the CDP transport and never leak a cookie or secret value. The fake
// transport would answer a cookie read with injectionCanary, so a single
// dispatch would fail the test loudly.
func TestF6GAPSEC05InjectionCorpus(t *testing.T) {
	t.Parallel()
	cases := cdpStructuralInjectionCases()
	cases = append(cases, cdpApprovalInjectionCases()...)
	if len(cases) != 20 {
		t.Fatalf("injection corpus = %d variants, want 20", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cdpRunInjection(t, tc)
		})
	}
	t.Run("encoded and unexpected method shapes", func(t *testing.T) {
		t.Parallel()
		cdpUnexpectedMethods(t)
	})
	t.Run("percent-encoded upload traversal", func(t *testing.T) {
		t.Parallel()
		cdpEncodedTraversal(t)
	})
}

// cdpStructuralInjectionCases are refused by the allowlist or the payload
// policy before approval exists.
func cdpStructuralInjectionCases() []injectionCase {
	return []injectionCase{
		{
			name: "navigation outside the allowlist",
			args: `{"method":"Page.navigate","params":{"url":"https://app.example/","hint":"` + injectionCanary + `"}}`,
		},
		{
			name: "screenshot outside the allowlist",
			args: `{"method":"Page.captureScreenshot","params":{"note":"` + injectionCanary + `"}}`,
		},
		{
			name: "target created at the EC2 metadata address",
			args: `{"method":"Target.createTarget","params":{"url":"http://169.254.169.254/latest/meta-data/"}}`,
		},
		{
			name: "target created at the Alibaba metadata address",
			args: `{"method":"Target.createTarget","params":{"url":"http://100.100.100.200/latest/meta-data/"}}`,
		},
		{
			name: "target created at a file URL",
			args: `{"method":"Target.createTarget","params":{"url":"file:///etc/passwd"}}`,
		},
		{
			name: "upload path traversal",
			args: `{"method":"DOM.setFileInputFiles","params":{"nodeId":1,"files":["../../etc/passwd"]}}`,
		},
		{
			name: "upload of a data URL",
			args: `{"method":"DOM.setFileInputFiles","params":{"nodeId":1,"files":["data:text/html;base64,PHNjcmlwdD4="]}}`,
		},
		{
			name: "local file reference outside the upload method",
			args: `{"method":"DOM.getBoxModel","params":{"nodeId":1,"hint":"file:///root/.ssh/id_rsa"}}`,
		},
		{
			name: "script URL smuggled into a safe method's parameters",
			args: `{"method":"Page.getLayoutMetrics","params":{"nodeId":1,"hint":"javascript:document.cookie"}}`,
		},
		{
			name: "lowercase domain in the method name",
			args: `{"method":"page.getLayoutMetrics","params":{"marker":"` + injectionCanary + `"}}`,
		},
		{
			name: "leading whitespace in the method name",
			args: `{"method":" Page.getLayoutMetrics","params":{"marker":"` + injectionCanary + `"}}`,
		},
		{
			name: "null method",
			args: `{"method":null,"params":{"marker":"` + injectionCanary + `"}}`,
		},
	}
}

// cdpApprovalInjectionCases are sensitive families refused by the approval
// seam: the fake answer is deny, so they never dispatch.
func cdpApprovalInjectionCases() []injectionCase {
	return []injectionCase{
		{
			name:   "cookie read through Runtime.evaluate without approval",
			args:   `{"method":"Runtime.evaluate","params":{"expression":"document.cookie","secret":"` + injectionCanary + `"}}`,
			answer: host.ApprovalDeny, approval: 1,
		},
		{
			name:   "Network.getCookies without approval",
			args:   `{"method":"Network.getCookies","params":{"marker":"` + injectionCanary + `"}}`,
			answer: host.ApprovalDeny, approval: 1,
		},
		{
			name:   "Storage.getCookies without approval",
			args:   `{"method":"Storage.getCookies","params":{"marker":"` + injectionCanary + `"}}`,
			answer: host.ApprovalDeny, approval: 1,
		},
		{
			name:   "Runtime.callFunctionOn without approval",
			args:   `{"method":"Runtime.callFunctionOn","params":{"functionDeclaration":"function(){return ` + injectionCanary + `}"}}`,
			answer: host.ApprovalDeny, approval: 1,
		},
		{
			name:   "Fetch.enable without approval",
			args:   `{"method":"Fetch.enable","params":{"patterns":[{"urlPattern":"*"}]}}`,
			answer: host.ApprovalDeny, approval: 1,
		},
		{
			name:   "Fetch.continueRequest without approval",
			args:   `{"method":"Fetch.continueRequest","params":{"requestId":"` + injectionCanary + `"}}`,
			answer: host.ApprovalDeny, approval: 1,
		},
		{
			name:   "Network.setCookie without approval",
			args:   `{"method":"Network.setCookie","params":{"name":"session","value":"` + injectionCanary + `"}}`,
			answer: host.ApprovalDeny, approval: 1,
		},
		{
			name:   "Debugger.enable without approval",
			args:   `{"method":"Debugger.enable","params":{"marker":"` + injectionCanary + `"}}`,
			answer: host.ApprovalDeny, approval: 1,
		},
	}
}

// cdpRunInjection proves one variant is refused before dispatch and leaks
// nothing the operator or a log could read.
func cdpRunInjection(t *testing.T, tc injectionCase) {
	t.Helper()
	f := newFake()
	// A dispatched read would return the canary in the result.
	f.results["Network.getCookies"] = json.RawMessage(`{"cookies":[{"name":"session","value":"` + injectionCanary + `"}]}`)
	f.results["Storage.getCookies"] = json.RawMessage(`{"cookies":[{"name":"session","value":"` + injectionCanary + `"}]}`)
	appr := &fakeApprovals{answer: tc.answer}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})

	res, err := m.Invoke(t.Context(), "browser_cdp", json.RawMessage(tc.args), host.Env{CallID: "gapsec05"})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" {
		t.Fatalf("browser_cdp = %q, %v; want denied", res.Content, err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("transport calls = %v, want none", f.calls)
	}
	reqs := appr.requests()
	if len(reqs) != tc.approval {
		t.Fatalf("approval requests = %d, want %d", len(reqs), tc.approval)
	}
	if strings.Contains(res.Content, injectionCanary) ||
		strings.Contains(he.Message, injectionCanary) || strings.Contains(he.Hint, injectionCanary) {
		t.Fatalf("refusal leaked the canary: result=%q error=%q hint=%q", res.Content, he.Message, he.Hint)
	}
	for _, req := range reqs {
		if strings.Contains(req.Preview, injectionCanary) {
			t.Fatalf("approval preview leaked the canary: %q", req.Preview)
		}
	}
}

// cdpUnexpectedMethods covers shapes the method pattern must refuse without
// asking for approval.
func cdpUnexpectedMethods(t *testing.T) {
	t.Helper()
	for _, args := range []string{
		`{"method":"Page.getlayoutMetrics"}`,
		`{"method":"Page.getLayoutMetrics "}`,
		`{"method":""}`,
		`{"method":" "}`,
		`{"method":"Network.GetCookies"}`,
		`{"method":123}`,
	} {
		f := newFake()
		appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
		m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
		if _, err := m.Invoke(t.Context(), "browser_cdp", json.RawMessage(args), host.Env{}); err == nil {
			t.Fatalf("%s accepted", args)
		}
		if len(f.calls) != 0 || len(appr.requests()) != 0 {
			t.Fatalf("%s: calls=%v approvals=%d, want none", args, f.calls, len(appr.requests()))
		}
	}
}

// cdpEncodedTraversal proves a percent-encoded `..` is still a traversal.
func cdpEncodedTraversal(t *testing.T) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
	args := `{"method":"DOM.setFileInputFiles","params":{"nodeId":1,"files":["..%2f..%2fetc%2fpasswd"]}}`
	if _, err := m.Invoke(t.Context(), "browser_cdp", json.RawMessage(args), host.Env{}); err == nil {
		t.Fatal("encoded traversal accepted")
	}
	if len(f.calls) != 0 || len(appr.requests()) != 0 {
		t.Fatalf("calls=%v approvals=%d, want none", f.calls, len(appr.requests()))
	}
}

// approvalMode is one approval answer for a sensitive method.
type approvalMode struct {
	name   string
	answer host.Approval
	calls  int
}

// TestF6GAPSEC05SensitiveApproval (GAP-SEC-05): every sensitive method on the
// reviewed allowlist needs one allow-once approval per call. Without it, and
// with a session approval, nothing is dispatched.
func TestF6GAPSEC05SensitiveApproval(t *testing.T) {
	t.Parallel()
	probes := cdpSensitiveProbes()
	if err := cdpSensitiveClasses(probes); err != nil {
		t.Fatal(err)
	}
	for _, method := range probes {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			cdpApprovalModes(t, method)
		})
	}
}

// cdpSensitiveProbes is every reviewed sensitive method plus one probe per
// sensitive domain.
func cdpSensitiveProbes() []string {
	probes := slices.Clone(cdpReviewedSensitive())
	for _, probe := range []string{"Storage.getCookies", "Debugger.enable", "Fetch.enable", "Target.getTargets", "Browser.getVersion"} {
		if !slices.Contains(probes, probe) {
			probes = append(probes, probe)
		}
	}
	return probes
}

// cdpSensitiveClasses proves every probe is a sensitive allowlisted method and
// no safe method is also sensitive.
func cdpSensitiveClasses(probes []string) error {
	for _, method := range probes {
		if sensitive, allowed := cdpMethodClass(method); !sensitive || !allowed {
			return errors.New(method + ": not a sensitive allowlisted method")
		}
	}
	for _, method := range cdpReviewedSafe() {
		if cdpSensitiveMethod(method) || cdpSensitiveDomain(method) {
			return errors.New(method + ": both safe and sensitive")
		}
	}
	return nil
}

// cdpApprovalModes runs the approval matrix for one sensitive method.
func cdpApprovalModes(t *testing.T, method string) {
	t.Helper()
	for _, mode := range []approvalMode{
		{"denied without approval", host.ApprovalDeny, 0},
		{"denied with a session approval", host.ApprovalAllowSession, 0},
		{"one dispatch with allow-once", host.ApprovalAllowOnce, 1},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			cdpApprovalMode(t, method, mode)
		})
	}
}

// cdpApprovalMode proves one approval answer for one sensitive method.
func cdpApprovalMode(t *testing.T, method string, mode approvalMode) {
	t.Helper()
	f := newFake()
	appr := &fakeApprovals{answer: mode.answer}
	m := newModule(t.Context(), t, new(moduleConfig()), Deps{Dial: dialTo(f), Approvals: appr})
	res, err := m.Invoke(t.Context(), "browser_cdp",
		json.RawMessage(`{"method":"`+method+`"}`), host.Env{CallID: "gapsec05"})
	if mode.calls == 0 {
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "denied" {
			t.Fatalf("err = %v, want denied", err)
		}
		if len(f.calls) != 0 {
			t.Fatalf("transport calls = %v, want none", f.calls)
		}
		reqs := appr.requests()
		if len(reqs) != 1 || !strings.Contains(reqs[0].Preview, "sensitive") {
			t.Fatalf("approval requests = %+v, want one sensitive preview", reqs)
		}
		return
	}
	if err != nil {
		t.Fatalf("approved call: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0] != method {
		t.Fatalf("transport calls = %v, want exactly [%s]", f.calls, method)
	}
	if res.Meta["sensitive"] != true || res.Meta["method"] != method {
		t.Fatalf("meta = %v", res.Meta)
	}
}
