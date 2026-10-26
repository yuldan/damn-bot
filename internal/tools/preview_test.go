package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// previewAuditor records approval requests and checks the world at prompt
// time: it denies, so a case that reaches execution fails loudly.
type previewAuditor struct {
	t      *testing.T
	before func(ApprovalRequest)
	mu     sync.Mutex
	reqs   []ApprovalRequest
}

//nolint:gocritic // the Approver interface fixes the by-value request
func (a *previewAuditor) Approve(_ context.Context, req ApprovalRequest) (Approval, error) {
	if a.before != nil {
		a.before(req)
	}
	a.mu.Lock()
	a.reqs = append(a.reqs, req)
	a.mu.Unlock()
	return ApprovalDeny, nil
}

func (a *previewAuditor) last() *ApprovalRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.reqs) == 0 {
		return &ApprovalRequest{}
	}
	return &a.reqs[len(a.reqs)-1]
}

func (a *previewAuditor) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.reqs)
}

// dangerousShell returns every destructive/P26 command the suite exercises.
// The classes come from the classifier's own table; the test asserts each one
// is dangerous rather than trusting a hand-written flag.
func dangerousShell() []string {
	return []string{
		"rm -rf ~", "rm -rf /", "shred -u secret", "sudo rm -rf /", "sudo dd if=/dev/zero of=/dev/sdz",
		"dd if=/dev/zero of=/dev/sdz", "mkfs.ext4 /dev/sdz", "fdisk /dev/sdz", "parted /dev/sdz",
		"wipefs -a /dev/sdz", "git reset --hard HEAD~1", "git clean -fdx", "git filter-branch --all",
		"bash -c 'rm -rf /'", "eval 'rm -rf /'", "echo hi > /etc/x", "docker system prune -af",
		"kubectl delete -f x.yaml", "kubectl delete pod x", "terraform destroy", "terraform apply",
		"crontab -r", "reboot", "shutdown -h now", "halt", "git push origin main", "git push --force",
		"git push -f origin main", "git push --force-with-lease", "npm publish", "npm unpublish x",
		"npm install -g x", "pnpm publish", "yarn publish", "bun publish", "cargo publish", "gem push x",
		"twine upload dist/*", "docker push x", "pip install x", "apt-get install jq", "brew install jq",
		"sudo apt install jq", "ssh host 'rm -rf /'", "scp a host:/x", "rsync -a a host:/x",
		"curl -X POST https://api.example.com -d @a.json", "curl -X DELETE https://api.example.com/x",
		"gh release delete v1", "gh pr merge 1", "gh secret set X", "glab mr merge 1",
		"claude --dangerously-skip-permissions -p x", "codex --yolo x", "gemini -y x",
		"opencode --yolo x", "cursor-agent --dangerously-skip-permissions x",
		"link-cli spend-request 10", "stripe charges create", "stripe transfers create",
		"stripe payment_intents create", "gh sponsor x", "cast send 0x0", "solana transfer x",
	}
}

// fileCase is one dangerous code-tool approval.
type fileCase struct {
	name string
	args string
	want []string // substrings the preview must carry
}

// dangerousFiles returns the write/edit cases; every one mutates the workspace.
func dangerousFiles() []fileCase {
	return []fileCase{
		{"overwrite", `{"path":"a.txt","content":"beta\n"}`, []string{"write a.txt", "+ beta"}},
		{"create", `{"path":"new.txt","content":"hello\n"}`, []string{"write new.txt", "+ hello"}},
		{"edit exact", `{"path":"a.txt","tag":"deadbeef","edits":[{"old_string":"alpha","new_string":"beta"}]}`, []string{"edit a.txt", "- alpha", "+ beta"}},
		{"edit line range", `{"path":"a.txt","tag":"deadbeef","edits":[{"start_line":1,"end_line":1,"new_text":"gamma"}]}`, []string{"edit a.txt", "(line range)", "+ gamma"}},
		{"edit many", `{"path":"a.txt","tag":"deadbeef","edits":[` + manyEdits(12) + `]}`, []string{"edit a.txt", "+2 more edits"}},
	}
}

func manyEdits(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = `{"old_string":"alpha","new_string":"beta"}`
	}
	return strings.Join(parts, ",")
}

// TestPreviewBeforeDangerous: 100% of the dangerous corpus (69 cases today)
// reaches the approver with a non-empty preview before any execution, and
// every case passes the approval contract (ADR-197, ADR-199, SPEC §18.2).
func TestPreviewBeforeDangerous(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	w.write("a.txt", "alpha\n")
	runner := &fakeRunner{}
	audit := &previewAuditor{t: t}
	d, j, _ := w.dispatcher(t, ModeAsk, audit, dangerShell(t, w, runner))
	shellCmds := dangerousShell()
	for i, cmd := range shellCmds {
		audit.before = func(ApprovalRequest) {
			if runner.calls != 0 {
				t.Fatalf("a process ran before approval (%q)", cmd)
			}
		}
		out := d.Dispatch(context.Background(), Call{
			TurnID: "T", CallID: fmt.Sprintf("shell-%d", i), Name: "shell",
			Args: json.RawMessage(`{"command":` + quoteJSON(cmd) + `}`),
		})
		requireDenied(t, out, cmd)
		requireShellPreview(t, audit.last())
	}
	files := dangerousFiles()
	for _, c := range files {
		before := w.read("a.txt")
		exists := fileExists(filepath.Join(w.root, "new.txt"))
		audit.before = func(ApprovalRequest) {
			if got := w.read("a.txt"); got != before {
				t.Fatalf("%s: a.txt changed before approval", c.name)
			}
			if fileExists(filepath.Join(w.root, "new.txt")) != exists {
				t.Fatalf("%s: new.txt appeared before approval", c.name)
			}
		}
		out := d.Dispatch(context.Background(), Call{
			TurnID: "T", CallID: "file-" + c.name, Name: fileToolName(c.args),
			Args: json.RawMessage(c.args),
		})
		requireDenied(t, out, c.name)
		requireFilePreview(t, audit.last(), c.want)
	}
	dangerous := len(shellCmds) + len(files)
	if dangerous < 50 {
		t.Fatalf("dangerous corpus = %d, want >= 50", dangerous)
	}
	if got := audit.count(); got != dangerous {
		t.Fatalf("approver saw %d requests, want %d (one per dangerous case)", got, dangerous)
	}
	if runner.calls != 0 {
		t.Fatalf("dangerous commands spawned %d processes", runner.calls)
	}
	if dc, rc := j.counts(); dc != 0 || rc != 0 {
		t.Fatalf("journal = %d/%d after denials", dc, rc)
	}
}

func dangerShell(t *testing.T, w *workspace, runner *fakeRunner) Tool {
	t.Helper()
	sh, err := NewShell(&ShellConfig{
		Runner: runner, Spill: newMemSpill(), Redactor: testRedactor(),
		Roots: []string{w.root}, Home: w.root, Cwd: w.root,
		ParentEnv: os.Environ(), EnvPolicy: testEnvPolicy(), WorkspaceTrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func requireDenied(t *testing.T, out Outcome, label string) {
	t.Helper()
	if out.Err == nil {
		t.Fatalf("%q was not denied: %s", label, out.Render())
	}
}

func requireShellPreview(t *testing.T, req *ApprovalRequest) {
	t.Helper()
	if err := req.Validate(); err != nil {
		t.Fatalf("shell request violates the contract: %v", err)
	}
	if !PreviewRequired(req.Effect, req.Class) {
		t.Fatalf("shell request is not a dangerous class: %s", req.Class)
	}
	if req.Origin != OriginRun || !strings.HasPrefix(req.Preview, "command: ") || !strings.Contains(req.Preview, "\nclass: ") {
		t.Fatalf("shell request = %+v", req)
	}
	for _, label := range []string{"destructive", "irreversible", "monetary", "delegated_agent_bypass"} {
		if strings.Contains(req.Class, label) && !strings.Contains(req.Preview, "class: "+label) {
			t.Fatalf("preview does not name class %s: %q", label, req.Preview)
		}
	}
}

func requireFilePreview(t *testing.T, req *ApprovalRequest, want []string) {
	t.Helper()
	if err := req.Validate(); err != nil {
		t.Fatalf("file request violates the contract: %v", err)
	}
	if !PreviewRequired(req.Effect, req.Class) || req.Origin != OriginRun {
		t.Fatalf("file request = %+v", req)
	}
	for _, s := range want {
		if !strings.Contains(req.Preview, s) {
			t.Fatalf("preview lacks %q: %q", s, req.Preview)
		}
	}
}

func fileToolName(args string) string {
	if strings.Contains(args, `"edits"`) {
		return "edit_file"
	}
	return "write_file"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestPreviewDangerousDeniedKeepsPreview: a hard denial (headless) still
// carries the preview in the decision, even though no approver is consulted.
func TestPreviewDangerousDeniedKeepsPreview(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	w.write("a.txt", "alpha\n")
	d, _, _ := w.dispatcher(t, ModeHeadless, nil, dangerShell(t, w, &fakeRunner{}))
	cases := []Call{
		{Name: "shell", Args: json.RawMessage(`{"command":"rm -rf ~"}`)},
		{Name: "write_file", Args: json.RawMessage(`{"path":"a.txt","content":"beta\n"}`)},
		{Name: "write_file", Args: json.RawMessage(`{"path":"new.txt","content":"x"}`)},
	}
	for i, c := range cases {
		c.CallID = fmt.Sprintf("deny-%d", i)
		p, perr := d.Prepare(context.Background(), c)
		if perr != nil {
			t.Fatalf("%s prepare: %v", c.Name, perr)
		}
		if p.Decision.Requirement != RequireDeny {
			t.Fatalf("%s requirement = %v, want deny", c.Name, p.Decision.Requirement)
		}
		if !PreviewRequired(p.Effect, p.Decision.Class) || p.Decision.Preview == "" {
			t.Fatalf("%s dangerous denial lacks a preview: %+v", c.Name, p.Decision)
		}
	}
	if got := w.read("a.txt"); got != "alpha\n" {
		t.Fatalf("headless denial wrote a.txt: %q", got)
	}
}

// TestPreviewCap: one preview is bounded at MaxPreview runes with an explicit
// truncation marker, for both shell and code tools.
func TestPreviewCap(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	long := strings.Repeat("a", 20000)
	d, _, _ := w.dispatcher(t, ModeAuto, nil, dangerShell(t, w, &fakeRunner{}))
	calls := []Call{
		{Name: "shell", Args: json.RawMessage(`{"command":"echo ` + long + `"}`)},
		{Name: "write_file", Args: json.RawMessage(`{"path":"big.txt","content":"` + strings.Repeat("b\\n", 5000) + `"}`)},
	}
	for i, c := range calls {
		c.CallID = fmt.Sprintf("cap-%d", i)
		p, err := d.Prepare(context.Background(), c)
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		n := len([]rune(p.Decision.Preview))
		if n > MaxPreview+len("… (preview truncated)")+2 {
			t.Fatalf("%s preview = %d runes", c.Name, n)
		}
		if c.Name == "shell" && !strings.Contains(p.Decision.Preview, "preview truncated") {
			t.Fatalf("shell preview not truncated: %q", p.Decision.Preview)
		}
	}
}
