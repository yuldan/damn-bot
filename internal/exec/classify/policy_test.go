package classify

import (
	"fmt"
	"strings"
	"testing"
)

func goBinary(string) (BinaryID, bool) {
	return BinaryID{Path: "/usr/bin/go", Dev: 1, Ino: 42, Size: 1 << 20, MtimeNs: 7}, true
}

func allowRequest(cmd string, entries ...AllowEntry) *AllowRequest {
	id, _ := goBinary("go")
	return &AllowRequest{
		Command: cmd, Cwd: testRoot, Roots: []string{testRoot}, Entries: entries, Resolve: goBinary,
		SessionStart: map[string]BinaryID{"go": id, "git": id},
	}
}

// approvalRow is one row of the ADR-079 table (default context:
// shell_allow = [["go","test","./..."]], trusted workspace, clean turn, no
// unapproved write in the session).
type approvalRow struct {
	n       int
	cmd     string
	verdict *Verdict // code-tool rows: verdict of the sandbox, not the classifier
	label   Class
	flags   Flag
	mod     func(c *Context, r *AllowRequest)
	want    string // decision codes per mode
}

func shellApprovalRows() []approvalRow {
	session := func(c *Context, _ *AllowRequest) { c.Session = true }
	return []approvalRow{
		{1, "ls -la src", nil, Mutating, 0, nil, "QQQDA"},
		{2, "cat README.md | wc -l", nil, Mutating, 0, nil, "QQQDA"},
		{3, "git status", nil, Mutating, 0, nil, "QQQDA"},
		{4, "cat ~/.ssh/id_rsa", nil, Mutating, DeniedPath, nil, "EEEEE"},
		{5, "sort --files0-from=- <<< ~/.ssh/id_rsa", nil, Unknown, 0, nil, "PPPDD"},
		{6, "grep -R token .", nil, Mutating, 0, nil, "QQQDA"},
		{7, "printf -v PATH %s ./bin", nil, Mutating, 0, session, "QQQDA"},
		{8, "cd ~ && cat .ssh/id_rsa", nil, Unknown, 0, nil, "PPPDD"},
		{9, "go test ./...", nil, Mutating, 0, nil, "AAAAA"},
		{10, "go test ./... -run X", nil, Mutating, 0, nil, "QQQDA"},
		{11, "go test ./... | tee out", nil, Mutating, 0, nil, "QQQDA"},
		{12, "GOFLAGS=-x go test ./...", nil, Mutating, 0, nil, "QQQDA"},
		{13, "go test ./...", nil, Mutating, 0, func(c *Context, _ *AllowRequest) { c.WorkspaceTrusted = false }, "QQQDD"},
		{14, "go test ./...", nil, Mutating, 0, func(c *Context, _ *AllowRequest) { c.TurnTainted = true }, "QQQDD"},
		{15, "go test ./...", nil, Mutating, 0, func(c *Context, _ *AllowRequest) { c.WroteWithoutApproval = true }, "QQQDA"},
		{16, "./go test ./...", nil, Mutating, 0, nil, "QQQDA"},
		{17, "go test ./...", nil, Mutating, 0, session, "QQQDA"},
		{18, "go test ./...", nil, Mutating, 0, func(c *Context, r *AllowRequest) { c.Session, c.CwdInRoot, r.Cwd = true, false, "/tmp" }, "QQQDD"},
		{19, "echo 'rm -rf ~' | bash", nil, Unknown, 0, nil, "PPPDD"},
		{20, "rm -rf ~", nil, Destructive, 0, nil, "PPPDD"},
		{21, "git push origin main", nil, Irreversible, 0, nil, "PPPDD"},
		{22, "npm publish", nil, Irreversible, 0, nil, "PPPDD"},
		{23, "curl -X POST https://api.example.com -d @a.json", nil, Irreversible, Network, nil, "PPPDD"},
		{24, "curl -s https://example.com", nil, Mutating, Network, nil, "QQQDA"},
		{25, "sudo apt install jq", nil, Irreversible, Privileged | Network, nil, "PPPDD"},
		{26, "sudo ls /root", nil, Mutating, Privileged | OutsideRead, nil, "CCCDD"},
		{27, "claude --dangerously-skip-permissions -p x", nil, DelegatedAgentBypass, 0, nil, "PPPDD"},
		{28, "npm install", nil, Mutating, Network, nil, "QQQDA"},
		{29, "ssh host 'ls'", nil, Irreversible, Network, nil, "PPPDD"},
		{30, "git diff", nil, Mutating, 0, nil, "QQQDA"},
		// 31 is the output wrapper (TestShellApprovalModel/31 below).
		{32, "read_file ~/.ssh/id_rsa", &Verdict{Flags: DeniedPath}, Mutating, DeniedPath, nil, "EEEEE"},
		{33, "read_file ../notes.txt", &Verdict{Flags: OutsideRead}, Mutating, OutsideRead, nil, "PPPOO"},
		{34, "read_file link", &Verdict{Flags: OutsideRead}, Mutating, OutsideRead, nil, "PPPOO"},
		{35, "read_file /proc/self/task/1/environ", &Verdict{Flags: DeniedPath}, Mutating, DeniedPath, nil, "EEEEE"},
		{36, "go vet ./...", nil, Mutating, 0, func(c *Context, _ *AllowRequest) { c.Verify = true }, "QQQUA"},
	}
}

// TestShellApprovalModel: every row of the ADR-079 table, label and policy
// per mode exact (EXEC-AND-CODING §A.8 acceptance).
func TestShellApprovalModel(t *testing.T) {
	t.Parallel()
	for _, row := range shellApprovalRows() {
		t.Run(fmt.Sprint(row.n), func(t *testing.T) {
			t.Parallel()
			v := row.verdict
			if v == nil {
				got := Classify(row.cmd, testEnv())
				v = &got
			}
			if v.Class != row.label || v.Flags != row.flags {
				t.Fatalf("%q label = %s %v, want %s %v", row.cmd, v.Class, v.Flags.Names(), row.label, row.flags.Names())
			}
			var got strings.Builder
			for _, m := range modes() {
				c := Context{Mode: m, WorkspaceTrusted: true, CwdInRoot: true}
				r := allowRequest(row.cmd, AllowEntry{"go", "test", "./..."})
				if row.mod != nil {
					row.mod(&c, r)
				}
				c.AllowMatched = MatchAllow(r)
				got.WriteByte(decisionCode(Decide(v, &c)))
			}
			if got.String() != row.want {
				t.Fatalf("%q decisions = %s, want %s", row.cmd, got.String(), row.want)
			}
		})
	}
	t.Run("31", func(t *testing.T) {
		t.Parallel()
		v := Classify("cat x", testEnv())
		if src := OutputSource(&v, false); src != "workspace" {
			t.Fatalf("untrusted output source = %q, want workspace", src)
		}
	})
}

func TestOutputSource(t *testing.T) {
	t.Parallel()
	for cmd, want := range map[string]string{
		"cat x":                      "",
		"curl https://x.example":     "shell:network",
		"cat /etc/hosts":             "shell:outside_workspace",
		"curl -o /dev/null http://x": "shell:network",
	} {
		v := Classify(cmd, testEnv())
		if got := OutputSource(&v, true); got != want {
			t.Errorf("OutputSource(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// TestShellNoClassifierAuto (EXC-13): with an empty shell_allow no command
// of the corpus runs without approval in ask/auto-edit/auto, nor headless
// without --unsafe-auto-shell.
func TestShellNoClassifierAuto(t *testing.T) {
	t.Parallel()
	var cmds []string
	for _, c := range labelCorpus() {
		cmds = append(cmds, c.cmd, c.cmd+" | cat", "true && "+c.cmd)
	}
	if len(cmds) < 300 {
		t.Fatalf("corpus %d < 300", len(cmds))
	}
	for _, cmd := range cmds {
		v := Classify(cmd, testEnv())
		matched := MatchAllow(allowRequest(cmd))
		for _, m := range []Mode{ModeAsk, ModeAutoEdit, ModeAuto, ModeHeadless} {
			c := Context{Mode: m, AllowMatched: matched, WorkspaceTrusted: true, CwdInRoot: true}
			if d := Decide(&v, &c); d == AutoApprove || matched {
				t.Fatalf("%q in mode %d = %s with empty shell_allow", cmd, m, d)
			}
		}
	}
}

// TestUnsafeAutoShellFlag: --unsafe-auto-shell runs plain mutating (with
// or without network) commands only in a trusted, untainted workspace.
func TestUnsafeAutoShellFlag(t *testing.T) {
	t.Parallel()
	cases := map[string]Decision{
		"go build ./...":                 AutoApprove,
		"npm install":                    AutoApprove,
		"rm -rf ~":                       Denied,
		"echo x | bash":                  Denied,
		"git push":                       Denied,
		"stripe charges create":          Denied,
		"codex --yolo":                   Denied,
		"sudo make install":              Denied,
		"cat /etc/hosts":                 OutsideWorkspace,
		"cat ~/.aws/credentials":         DeniedPathError,
		"echo $STRIPE_KEY":               Denied,
		"curl http://169.254.169.254/x/": Denied,
	}
	for cmd, want := range cases {
		v := Classify(cmd, testEnv())
		c := Context{Mode: ModeUnsafeAutoShell, WorkspaceTrusted: true, CwdInRoot: true}
		if got := Decide(&v, &c); got != want {
			t.Errorf("%q = %s, want %s", cmd, got, want)
		}
		for _, mod := range []func(*Context){
			func(c *Context) { c.WorkspaceTrusted = false },
			func(c *Context) { c.TurnTainted = true },
			func(c *Context) { c.CwdInRoot = false },
			func(c *Context) { c.Mode = ModeHeadless },
		} {
			c := Context{Mode: ModeUnsafeAutoShell, WorkspaceTrusted: true, CwdInRoot: true}
			mod(&c)
			if got := Decide(&v, &c); got == AutoApprove {
				t.Errorf("%q auto despite %+v", cmd, c)
			}
		}
	}
}

// TestVerifyNeedsShellAllow (ADR-079 butir 5): verify runs without a human
// only when the command is exactly in shell_allow; otherwise interactive
// asks and headless reports unverified.
func TestVerifyNeedsShellAllow(t *testing.T) {
	t.Parallel()
	const cmd = "go vet ./..."
	v := Classify(cmd, testEnv())
	for _, allow := range []bool{false, true} {
		var entries []AllowEntry
		if allow {
			entries = append(entries, AllowEntry{"go", "vet", "./..."})
		}
		for _, m := range []Mode{ModeAsk, ModeAutoEdit, ModeAuto, ModeHeadless} {
			c := Context{Mode: m, Verify: true, WorkspaceTrusted: true, CwdInRoot: true, AllowMatched: MatchAllow(allowRequest(cmd, entries...))}
			want := Ask
			switch {
			case allow:
				want = AutoApprove
			case m == ModeHeadless:
				want = Unverified
			}
			if got := Decide(&v, &c); got != want {
				t.Errorf("allow=%v mode %d = %s, want %s", allow, m, got, want)
			}
		}
	}
	c := Context{Mode: ModeHeadless, Verify: true, CwdInRoot: true, AllowMatched: true}
	if got := Decide(&v, &c); got != Unverified {
		t.Errorf("untrusted verify = %s, want unverified", got)
	}
}

// TestPlanModeShell (ADR-079 butir 11): plan mode allows only an exact
// shell_allow match that is also non_mutating.
func TestPlanModeShell(t *testing.T) {
	t.Parallel()
	entries := []AllowEntry{{"git", "status"}, {"go", "test", "./..."}}
	for cmd, want := range map[string]Decision{
		"git status":    AutoApprove,
		"go test ./...": Denied,
		"ls":            Denied,
		"git push":      Denied,
		"cat ~/.ssh/x":  DeniedPathError,
	} {
		v := Classify(cmd, testEnv())
		for _, m := range modes() {
			c := Context{Mode: m, PlanMode: true, WorkspaceTrusted: true, CwdInRoot: true, AllowMatched: MatchAllow(allowRequest(cmd, entries...))}
			if got := Decide(&v, &c); got != want {
				t.Errorf("plan %q mode %d = %s, want %s", cmd, m, got, want)
			}
		}
	}
	v := Classify("git status", testEnv())
	c := Context{Mode: ModeAuto, PlanMode: true, CwdInRoot: true, AllowMatched: true}
	if got := Decide(&v, &c); got != Denied {
		t.Errorf("plan untrusted = %s, want denied", got)
	}
}

// TestNoReadOnlyClass (policy side of TestNoAutoClassReadonlyDecision,
// ADR-079 butir 10): no class or decision is read_only/auto_class_readonly.
func TestNoReadOnlyClass(t *testing.T) {
	t.Parallel()
	for c := Mutating; c <= DelegatedAgentBypass; c++ {
		if strings.Contains(c.String(), "read") {
			t.Fatalf("class %q", c)
		}
	}
	for d := Ask; d <= Unverified; d++ {
		if strings.Contains(d.String(), "readonly") || d.String() == "" {
			t.Fatalf("decision %q", d)
		}
	}
}
