package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec/classify"
)

// planShell builds a shell tool over a fake runner with an exact allow list.
func planShell(t *testing.T, w *workspace, runner *fakeRunner, allow []classify.AllowEntry) Tool {
	t.Helper()
	start := map[string]classify.BinaryID{}
	for _, a := range allow {
		start[a[0]] = classify.BinaryID{Path: "/bin/" + a[0], Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}
	}
	sh, err := NewShell(&ShellConfig{
		Runner: runner, Spill: newMemSpill(), Redactor: testRedactor(),
		Roots: []string{w.root}, Home: w.root, Cwd: w.root,
		ParentEnv: os.Environ(), EnvPolicy: testEnvPolicy(),
		Allow: allow, SessionStart: start,
		Resolve: func(argv0 string) (classify.BinaryID, bool) {
			id, ok := start[argv0]
			return id, ok
		},
		WorkspaceTrusted: true,
	})
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	return sh
}

// planCall is one plan-mode invocation.
func planCall(name, args string) Call {
	return Call{TurnID: "T", CallID: "c-" + name + "-" + args, Name: name, Args: json.RawMessage(args), PlanMode: true}
}

// TestPlanModeShellDispatch: during plan mode only an exact shell_allow match
// that the classifier labelled non_mutating runs; every other shell form and
// every non-shell mutating tool is denied before approval, journal and spawn
// (ADR-079 butir 11, ADR-203).
//
//nolint:gocognit,gocyclo // table-driven dispatch matrix: every row needs its own assertions
func TestPlanModeShellDispatch(t *testing.T) {
	t.Parallel()
	allow := []classify.AllowEntry{{"git", "status"}, {"go", "test", "./..."}}
	cases := []struct {
		name   string
		args   string
		run    bool
		class  string
		reason string
	}{
		{"exact non_mutating", `{"command":"git status"}`, true, "shell:mutating", ""},
		{"allowlisted but mutating", `{"command":"go test ./..."}`, false, "plan_mode", ""},
		{"not allowlisted", `{"command":"ls"}`, false, "plan_mode", ""},
		{"near match", `{"command":"git status -s"}`, false, "plan_mode", ""},
		{"pipeline", `{"command":"git status | tee out"}`, false, "plan_mode", ""},
		{"redirect", `{"command":"git status > out"}`, false, "plan_mode", ""},
		{"assignment", `{"command":"GOFLAGS=-x git status"}`, false, "plan_mode", ""},
		{"session", `{"command":"git status","session":"s"}`, false, "plan_mode", ""},
		{"stdin", `{"command":"git status","stdin":"x"}`, false, "plan_mode", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := newWorkspace(t)
			w.write("a.txt", "alpha\n")
			runner := &fakeRunner{}
			d, j, _ := w.dispatcher(t, ModeAuto, nil, planShell(t, w, runner, allow))
			p, err := d.Prepare(context.Background(), Call{Name: "shell", Args: json.RawMessage(c.args), PlanMode: true})
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if p.Decision.Class != c.class {
				t.Fatalf("class = %q, want %q", p.Decision.Class, c.class)
			}
			if !c.run && (p.Decision.Requirement != RequireDeny || p.Decision.PlanAllowed) {
				t.Fatalf("decision = %+v, want deny without PlanAllowed", p.Decision)
			}
			out := d.Dispatch(context.Background(), planCall("shell", c.args))
			if c.run {
				if out.Err != nil {
					t.Fatalf("allowed command: %v", out.Err)
				}
				if runner.calls != 1 {
					t.Fatalf("allowed command spawned %d processes", runner.calls)
				}
				if dc, rc := j.counts(); dc != 1 || rc != 1 {
					t.Fatalf("journal = %d/%d, want 1/1", dc, rc)
				}
				return
			}
			if out.Err == nil {
				t.Fatalf("denied command rendered success: %s", out.Content)
			}
			if runner.calls != 0 {
				t.Fatalf("denied command spawned %d processes", runner.calls)
			}
			if dc, rc := j.counts(); dc != 0 || rc != 0 {
				t.Fatalf("denied command journaled %d/%d", dc, rc)
			}
		})
	}
}

// TestPlanModeDeviationMatrix: 35 deviations from the plan-mode shell rule are
// all rejected with zero executions and zero journal rows.
func TestPlanModeDeviationMatrix(t *testing.T) {
	t.Parallel()
	allow := []classify.AllowEntry{{"go", "test", "./..."}, {"git", "status"}}
	commands := []string{
		"go test ./...", "go build ./...", "go vet ./...", "go test ./... -run X", "./go test ./...",
		"ls", "ls -la", "pwd", "cat README.md", "git diff", "git log", "git status -s",
		"git status | tee out", "git status > out", "git status >> out", "git status && rm -rf x",
		"git status; echo done", "GOFLAGS=-x go test ./...", "echo $(date)", `bash -c 'go test ./...'`,
		`sh -c "rm -rf x"`, "find . -delete", "rm -rf ./x", "mv a b", "cp a b", "touch x",
		"mkdir x", "npm install", "sudo ls", "curl -X POST https://example.com -d @a.json",
		"ssh host ls", `python -c 'print(1)'`, `perl -e 'unlink "x"'`, "git clean -fdx", "chmod +x x",
	}
	if len(commands) < 30 {
		t.Fatalf("deviation corpus = %d, want >= 30", len(commands))
	}
	w := newWorkspace(t)
	w.write("a.txt", "alpha\n")
	runner := &fakeRunner{}
	d, j, _ := w.dispatcher(t, ModeAuto, nil, planShell(t, w, runner, allow))
	before := treeState(t, w.root)
	for i, cmd := range commands {
		args := `{"command":` + quoteJSON(cmd) + `}`
		out := d.Dispatch(context.Background(), Call{TurnID: "T", CallID: fmt.Sprintf("d%d", i), Name: "shell", Args: json.RawMessage(args), PlanMode: true})
		if out.Err == nil {
			t.Fatalf("%q was not denied: %s", cmd, out.Render())
		}
		if runner.calls != 0 {
			t.Fatalf("%q spawned %d processes", cmd, runner.calls)
		}
		if dc, rc := j.counts(); dc != 0 || rc != 0 {
			t.Fatalf("%q journaled %d/%d", cmd, dc, rc)
		}
	}
	if after := treeState(t, w.root); after != before {
		t.Fatalf("workspace changed during plan mode:\nbefore %s\nafter  %s", before, after)
	}
}

// TestPlanModeDenyAllMutating iterates the registry: every tool with a
// non-read effect is denied in plan mode and executes nothing.
func TestPlanModeDenyAllMutating(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	w.write("a.txt", "alpha\n")
	runner := &fakeRunner{}
	sh := planShell(t, w, runner, []classify.AllowEntry{{"git", "status"}})
	d, _, _ := w.dispatcher(t, ModeAuto, nil, sh, NewTodo())
	reg := d.reg
	before := treeState(t, w.root)
	ran := 0
	for _, e := range reg.entries {
		if e.Tool.Effect(json.RawMessage(`{}`)) == EffectRead {
			continue
		}
		args := planArgs(t, e.Doc.Name)
		out := d.Dispatch(context.Background(), Call{TurnID: "T", CallID: "m-" + e.Doc.Name, Name: e.Doc.Name, Args: args, PlanMode: true})
		if out.Err == nil {
			t.Fatalf("%s (effect %s) ran in plan mode: %s", e.Doc.Name, e.Tool.Effect(args), out.Render())
		}
		ran++
	}
	if ran == 0 {
		t.Fatal("registry iteration saw no mutating tool")
	}
	if runner.calls != 0 {
		t.Fatalf("plan mode spawned %d processes", runner.calls)
	}
	if after := treeState(t, w.root); after != before {
		t.Fatal("plan mode changed the workspace")
	}
}

// planArgs returns valid arguments for one registry tool.
func planArgs(t *testing.T, name string) json.RawMessage {
	t.Helper()
	switch name {
	case "write_file":
		return json.RawMessage(`{"path":"new.txt","content":"x"}`)
	case "edit_file":
		return json.RawMessage(`{"path":"a.txt","edits":[{"old_string":"alpha","new_string":"beta"}]}`)
	case "shell":
		// A mutating command: the allowlisted non_mutating shell case is the
		// positive control in TestPlanModeShellDispatch.
		return json.RawMessage(`{"command":"rm -rf a.txt"}`)
	case "todo":
		return json.RawMessage(`{"items":[{"id":"1","text":"x","status":"pending"}]}`)
	}
	t.Fatalf("no plan-mode arguments for %s", name)
	return nil
}

// TestPlanModeDeferredInvoke: tool_invoke is unwrapped before policy, so its
// mutating inner tool is denied by the same gate.
func TestPlanModeDeferredInvoke(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	cat, err := NewCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	j := &journalRec{}
	reg, err := NewRegistry(append(w.tools, NewToolInvoke())...)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDispatcher(&Config{
		Registry: reg, Journal: j, Spill: newMemSpill(), Redactor: testRedactor(),
		Mode: ModeAuto, WorkspaceTrusted: true, Cwd: w.root,
		Deferred: &Deferred{Catalog: cat},
	})
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"name":"write_file","arguments":{"path":"x.txt","content":"x"}}`)
	out := d.Dispatch(context.Background(), Call{TurnID: "T", CallID: "i1", Name: ToolInvokeName, Args: args, PlanMode: true})
	if out.Err == nil {
		t.Fatalf("tool_invoke inner write ran: %s", out.Render())
	}
	if _, err := os.Stat(filepath.Join(w.root, "x.txt")); err == nil {
		t.Fatal("tool_invoke wrote a file in plan mode")
	}
	if dc, rc := j.counts(); dc != 0 || rc != 0 {
		t.Fatalf("journal = %d/%d", dc, rc)
	}
}

// TestPlanModeNoSideEffects: the workspace tree, journal and process count are
// unchanged after every mutating tool is denied; the control (plan mode off)
// journals exactly one dispatch.
func TestPlanModeNoSideEffects(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	w.write("a.txt", "alpha\n")
	runner := &fakeRunner{}
	sh := planShell(t, w, runner, []classify.AllowEntry{{"git", "status"}})
	d, j, _ := w.dispatcher(t, ModeAuto, nil, sh, NewTodo())
	before := treeState(t, w.root)
	calls := []Call{
		planCall("write_file", `{"path":"w.txt","content":"x"}`),
		planCall("edit_file", `{"path":"a.txt","edits":[{"old_string":"alpha","new_string":"beta"}]}`),
		planCall("shell", `{"command":"rm -rf a.txt"}`),
		planCall("todo", `{"items":[{"id":"1","text":"x","status":"done"}]}`),
	}
	for i := range calls {
		calls[i].CallID = fmt.Sprintf("n%d", i)
		out := d.Dispatch(context.Background(), calls[i])
		if out.Err == nil {
			t.Fatalf("%s ran in plan mode", calls[i].Name)
		}
	}
	if after := treeState(t, w.root); after != before {
		t.Fatalf("workspace changed: %s -> %s", before, after)
	}
	if dc, rc := j.counts(); dc != 0 || rc != 0 {
		t.Fatalf("journal = %d/%d", dc, rc)
	}
	if runner.calls != 0 {
		t.Fatalf("spawned %d processes", runner.calls)
	}
	// Control: without plan mode the same write runs and journals once.
	out := d.Dispatch(context.Background(), Call{TurnID: "T", CallID: "c1", Name: "write_file", Args: json.RawMessage(`{"path":"w.txt","content":"x"}`)})
	if out.Err != nil {
		t.Fatalf("control write: %v", out.Err)
	}
	if dc, rc := j.counts(); dc != 1 || rc != 1 {
		t.Fatalf("control journal = %d/%d, want 1/1", dc, rc)
	}
}

// treeState hashes every path, mode and file content under root.
func treeState(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("stat %s: %w", p, ierr)
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return fmt.Errorf("rel %s: %w", p, rerr)
		}
		line := fmt.Sprintf("%s %o %d", rel, info.Mode(), info.Size())
		if !d.IsDir() {
			b, berr := os.ReadFile(p) //nolint:gosec // path comes from the test's t.TempDir walk
			if berr != nil {
				return fmt.Errorf("read %s: %w", p, berr)
			}
			sum := sha256.Sum256(b)
			line += " " + hex.EncodeToString(sum[:])
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}
