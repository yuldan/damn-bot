//go:build linux

package e2e

// P28 Speed-parity probe for the 93 FEATURE-PARITY rows that already have a
// Damn-Bot implementation. Every op runs against the real Damn-Bot surface:
// CLI rows start the built binary (same 1 warm-up + 5 cold starts as the
// hermes CLI protocol), slash rows dispatch through internal/gateway, and
// tool/assemble rows call the same handlers production wires. The three IDs
// that already carry a committed measurement (TOOL-045/046/047) are not
// re-measured here; bench/speed/cmd/damnbot-speed merges them.
//
// The probe writes one JSON file into $DAMNBOT_SPEED_OUT; it never copies a
// hermes number. Run it through bench/speed/run-damnbot.sh.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/cron"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/code"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/skills/catalog"
	"github.com/damnbot/damnbot/internal/tools"
)

type speedProbeResult struct {
	SamplesMS []float64 `json:"samples_ms"`
	Error     string    `json:"error,omitempty"`
	Source    string    `json:"source,omitempty"`
}

type speedProbeFile struct {
	Results map[string]speedProbeResult `json:"results"`
}

const (
	probeCLIRuns  = 5
	probeInproc   = 20
	probeWarmup   = 1
	probeTimeout  = 30 * time.Second
	probeToolKind = "tools"
)

func probeBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("DAMNBOT_SPEED_BIN")
	if bin == "" {
		t.Skip("DAMNBOT_SPEED_BIN unset (run bench/speed/run-damnbot.sh)")
	}
	return bin
}

// TestSpeedP20Probe measures the 93 implemented rows and writes one partial
// result file. It is opt-in so a normal e2e run stays fast.
func TestSpeedP20Probe(t *testing.T) {
	outDir := os.Getenv("DAMNBOT_SPEED_OUT")
	if outDir == "" {
		t.Skip("DAMNBOT_SPEED_OUT unset (run bench/speed/run-damnbot.sh)")
	}
	bin := probeBin(t)
	home := t.TempDir()
	for _, d := range []string{"config", "data", "state", "cache", ".claude"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"DAMNBOT_NO_UPDATE_CHECK=1",
	)
	out := speedProbeFile{Results: map[string]speedProbeResult{}}
	ctx := context.Background()

	for id, argv := range cliProbeOps() {
		out.Results[id] = measureCommand(ctx, bin, env, argv)
	}
	for id, result := range toolProbeOps(t) {
		out.Results[id] = result
	}
	for id, result := range assembleProbeOps(t) {
		out.Results[id] = result
	}
	for id, result := range slashProbeOps() {
		out.Results[id] = result
	}
	if got := len(out.Results); got != 89 {
		t.Fatalf("measured rows = %d, want 89 (93 minus the 4 internal/tui rows)", got)
	}
	writeProbe(t, outDir, "e2e.json", out)
}

func writeProbe(t *testing.T, dir, name string, v any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func measureCommand(ctx context.Context, bin string, env, argv []string) speedProbeResult {
	res := speedProbeResult{Source: "damnbot binary cold start: " + strings.Join(argv, " ")}
	for i := range probeWarmup + probeCLIRuns {
		rctx, cancel := context.WithTimeout(ctx, probeTimeout)
		cmd := osexec.CommandContext(rctx, bin, argv...)
		cmd.Env = env
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		started := time.Now()
		err := cmd.Run()
		elapsed := time.Since(started)
		cancel()
		var ee *osexec.ExitError
		if err != nil && !(errors.As(err, &ee) && (ee.ExitCode() == 0 || ee.ExitCode() == 1 || ee.ExitCode() == 2)) {
			res.Error = err.Error()
			return res
		}
		if i >= probeWarmup {
			res.SamplesMS = append(res.SamplesMS, float64(elapsed.Microseconds())/1000)
		}
	}
	return res
}

func measureInproc(fn func() error) speedProbeResult {
	res := speedProbeResult{}
	for i := range probeWarmup + probeInproc {
		started := time.Now()
		if err := fn(); err != nil {
			res.Error = err.Error()
			return res
		}
		if i >= probeWarmup {
			res.SamplesMS = append(res.SamplesMS, float64(time.Since(started).Nanoseconds())/1e6)
		}
	}
	return res
}

// cliProbeOps maps the CLI/OPS IDs to the Damn-Bot surface that implements
// the same command. Hermes-only spellings are translated (cua, gateway
// pairing, setup completion, sessions backup) and the source records it.
func cliProbeOps() map[string][]string {
	return map[string][]string{
		"AUTO-005": {"cron", "tick"},
		"CLI-006":  {"browser", "--help"},
		"CLI-012":  {"proxy", "providers"},
		"CLI-014":  {"setup", "--help"},
		"CLI-015":  {"whatsapp", "--help"},
		"CLI-016":  {"whatsapp-cloud", "--help"},
		"CLI-017":  {"slack", "--help"},
		"CLI-018":  {"send", "--help"},
		"CLI-023":  {"pause", "--help"},
		"CLI-024":  {"resume", "--help"},
		"CLI-025":  {"cron", "list"},
		"CLI-027":  {"peer", "list"},
		"CLI-029":  {"kanban", "list"},
		"CLI-030":  {"project", "list"},
		"CLI-032":  {"doctor", "--quick"},
		"CLI-036":  {"dump"},
		"CLI-037":  {"debug", "--help"},
		"CLI-039":  {"checkpoints", "list"},
		"CLI-040":  {"import", "--help"},
		"CLI-041":  {"import", "claude-code", "--dry-run"},
		"CLI-045":  {"gateway", "pairing", "list"},
		"CLI-047":  {"bundles", "list"},
		"CLI-049":  {"curator", "status"},
		"CLI-051":  {"journey", "list"},
		"CLI-052":  {"memory", "status"},
		"CLI-054":  {"cua", "--help"},
		"CLI-055":  {"mcp", "list"},
		"CLI-058":  {"insights"},
		"CLI-060":  {"monitoring", "status"},
		"CLI-063":  {"update", "--help"},
		"CLI-065":  {"acp", "--help"},
		"CLI-067":  {"setup", "completion", "bash"},
		"CLI-068":  {"dashboard", "--help"},
		"CLI-069":  {"serve", "--help"},
		"CLI-070":  {"desktop", "--help"},
		"CLI-073":  {"--help"},
		"CLI-074":  {"--version"},
		"MEM-031":  {"checkpoints", "list"},
		"OPS-001":  {"config", "profiles", "ls"},
		"OPS-006":  {"sessions", "backup", "ls"},
		"OPS-007":  {"config", "show"},
		// OPS-009 has no `logs` subcommand (parity row says so); doctor reads
		// the logging state and is the closest real surface.
		"OPS-009":   {"doctor", "--quick"},
		"OPS-010":   {"dump"},
		"OPS-011":   {"doctor", "--quick"},
		"OPS-016":   {"insights"},
		"OPS-038":   {"project", "list"},
		"SKILL-007": {"bundles", "list"},
		"SKILL-016": {"skills", "update"},
		"SKILL-018": {"skills", "audit"},
		"SKILL-021": {"curator", "status"},
	}
}

// --- tools -----------------------------------------------------------------

type probeIntegration struct {
	d    *tools.Dispatcher
	all  *tools.Registry
	file *tools.Registry
	ws   string
}

//nolint:funlen // the wiring order mirrors production; one helper keeps the probes honest.
func newProbeIntegration(t *testing.T) *probeIntegration {
	t.Helper()
	ws := t.TempDir()
	mustWrite := func(rel, body string) {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("patch.txt", "line one\nalpha\nline three\n")
	mustWrite("out.txt", "")
	mustWrite("agent/system_prompt.py", strings.Repeat("def build_system_prompt():\n    return 'x'\n\n", 60))
	mustWrite("agent/readme.md", "fixture\n")

	sb, err := sandbox.New(&sandbox.Config{Roots: []string{ws}, Home: ws})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := codetools.New(&codetools.Config{Sandbox: sb, Roots: []string{ws}, NewEOL: "lf", Home: ws})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	fileTools := tools.FileTools(engine, sb, nil)

	runner := exec.New(t.Context())
	t.Cleanup(func() { _ = runner.Close() })
	codeRunner, err := code.New(&code.Config{Backend: probeNetBackend{Backend: exec.NewLocal(runner), id: "local-net-probe"}, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	spill := &probeSpill{m: map[string][]byte{}}
	sh, err := tools.NewShell(&tools.ShellConfig{
		Runner: runner, Spill: spill, Redactor: redact.New(redact.Options{}),
		Roots: []string{ws}, Home: ws, Cwd: ws, ParentEnv: os.Environ(), EnvPolicy: &exec.EnvPolicy{},
		WorkspaceTrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	skillsMgr := probeSkills{}
	allTools := append(fileTools, sh,
		tools.NewExecuteCode(&tools.ExecuteCodeConfig{Runner: codeRunner, AutoLanguages: []string{"python"}}),
		tools.NewSkillsList(skillsMgr), tools.NewSkillView(skillsMgr),
		tools.NewCronJob(&cron.Admin{Store: probeCronStore{}}),
	)
	allTools = append(allTools, tools.NewProcessTools(&tools.ProcessConfig{Registry: exec.NewBackground()})...)
	all, err := tools.NewRegistry(allTools...)
	if err != nil {
		t.Fatal(err)
	}
	file, err := tools.NewRegistry(fileTools...)
	if err != nil {
		t.Fatal(err)
	}
	view, err := tools.NewSessionView("speed", "", "run", ws,
		[]string{"read_file", "write_file", "edit_file", "grep", "glob", "shell", "execute_code", "skill_view", "skills_list", "cronjob_manage"},
		[]tools.Capability{tools.CapExecuteCode})
	if err != nil {
		t.Fatal(err)
	}
	d, err := tools.NewDispatcher(&tools.Config{
		Registry: all, Journal: probeJournal{}, Spill: spill,
		Redactor: redact.New(redact.Options{}), Mode: tools.ModeAsk, Approver: probeApprover{},
		WorkspaceTrusted: true, Cwd: ws, Session: view,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &probeIntegration{d: d, all: all, file: file, ws: ws}
}

func toolProbeOps(t *testing.T) map[string]speedProbeResult {
	t.Helper()
	pi := newProbeIntegration(t)
	out := map[string]speedProbeResult{}
	call := func(id, name string, args string) {
		res := measureInproc(func() error {
			pi.d.BeginTurn()
			o := pi.d.Dispatch(t.Context(), tools.Call{TurnID: "speed", CallID: id + fmt.Sprint(time.Now().UnixNano()), Name: name, Args: json.RawMessage(args)})
			if o.Err != nil {
				return errors.New(string(o.Err.Code) + ": " + o.Err.Message)
			}
			return nil
		})
		res.Source = "internal/tools dispatcher (" + name + ")"
		out[id] = res
	}
	call("EXEC-001", "shell", `{"command":"echo speed"}`)
	call("EXEC-012", "execute_code", `{"language":"python","code":"print(sum(range(1000)))"}`)
	call("TOOL-022", "execute_code", `{"language":"python","code":"print(1)"}`)
	call("TOOL-025", "cronjob_manage", `{"action":"list"}`)
	pi.d.BeginTurn()
	out48 := measureInproc(func() error {
		if err := os.Remove(filepath.Join(pi.ws, "out.txt")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		pi.d.BeginTurn()
		o := pi.d.Dispatch(t.Context(), tools.Call{TurnID: "speed", CallID: "TOOL-048-" + fmt.Sprint(time.Now().UnixNano()),
			Name: "write_file", Args: json.RawMessage(`{"path":"out.txt","content":"0123456789abcdef\n"}`)})
		if o.Err != nil {
			return errors.New(string(o.Err.Code) + ": " + o.Err.Message)
		}
		return nil
	})
	out48.Source = "internal/tools dispatcher (write_file, reset each run)"
	out["TOOL-048"] = out48
	call("TOOL-075", "skill_view", `{"name":"arxiv"}`)
	call("TOOL-076", "skills_list", `{}`)
	call("TOOL-077", "process_list", `{}`)
	call("TOOL-078", "shell", `{"command":"true"}`)
	for _, row := range []struct{ id, name string }{
		{"SKCAT-002", "claude-code"}, {"SKCAT-003", "architecture-diagram"},
		{"SKCAT-004", "sdlc-review"}, {"SKCAT-005", "email-inbox-triage"},
		{"SKCAT-006", "gif-search"}, {"SKCAT-007", "obsidian"},
		{"SKCAT-008", "airtable"}, {"SKCAT-009", "arxiv"},
		{"SKCAT-010", "xurl"}, {"SKCAT-011", "codebase-inspection"},
		{"SKCAT-012", "blocked-page-recovery"}, {"SKILL-001", "codebase-inspection"},
	} {
		call(row.id, "skill_view", `{"name":"`+row.name+`"}`)
	}
	call("SKILL-002", "skills_list", `{}`)
	return out
}

// probeSkills serves the committed catalog through the real SkillManager
// contract; the tool's dispatch and formatting path is what is measured.
type probeSkills struct{}

func (probeSkills) ListSkills(_ context.Context, limit int) ([]tools.SkillInfo, error) {
	cat, err := catalog.Load()
	if err != nil {
		return nil, fmt.Errorf("probe skills: %w", err)
	}
	entries := cat.Entries()
	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}
	out := make([]tools.SkillInfo, len(entries))
	for i := range entries {
		out[i] = tools.SkillInfo{Name: entries[i].Name, Description: entries[i].Description}
	}
	return out, nil
}

func (probeSkills) ViewSkill(_ context.Context, name string) (tools.SkillDoc, error) {
	cat, err := catalog.Load()
	if err != nil {
		return tools.SkillDoc{}, fmt.Errorf("probe skills: %w", err)
	}
	e, err := cat.Lookup(name)
	if err != nil {
		return tools.SkillDoc{}, fmt.Errorf("probe skill %q: %w", name, err)
	}
	return tools.SkillDoc{Name: e.Name, Description: e.Description, Body: e.Description + "\n"}, nil
}

func (probeSkills) ManageSkill(context.Context, *tools.SkillChange) (tools.SkillResult, error) {
	return tools.SkillResult{}, errors.New("skill_manage not measured")
}

type probeJournal struct{}

func (probeJournal) ToolDispatch(context.Context, string, string, string, tools.Effect, [32]byte) error {
	return nil
}
func (probeJournal) ToolResult(context.Context, string, string, string, bool, string, string) error {
	return nil
}

type probeSpill struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *probeSpill) Put(_ context.Context, data []byte) (tools.BlobRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := fmt.Sprintf("%x", sha256Sum(data))
	s.m[sum] = append([]byte(nil), data...)
	return tools.BlobRef{SHA: sum, Size: int64(len(data))}, nil
}

func (s *probeSpill) Get(_ context.Context, sha string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[sha]
	if !ok {
		return nil, errors.New("blob not found")
	}
	return b, nil
}

type probeCronStore struct{}

func (probeCronStore) Jobs(context.Context) ([]cron.Job, error) { return nil, nil }
func (probeCronStore) State(context.Context, string) (cron.State, error) {
	return cron.State{}, nil
}
func (probeCronStore) Active(context.Context, string) (cron.Active, error) {
	return cron.Active{}, nil
}
func (probeCronStore) Claim(context.Context, string, time.Time, time.Time, string) (bool, error) {
	return true, nil
}
func (probeCronStore) TakeQueued(context.Context, string, time.Time) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (probeCronStore) Record(context.Context, string, time.Time, *cron.Run) error { return nil }
func (probeCronStore) Finish(context.Context, string, time.Time, *cron.Run) error { return nil }
func (probeCronStore) Create(_ context.Context, j *cron.Job) (cron.Job, bool, error) {
	return *j, true, nil
}
func (probeCronStore) Update(context.Context, *cron.Job) error { return nil }
func (probeCronStore) SetEnabled(context.Context, string, bool, string) error {
	return nil
}
func (probeCronStore) Remove(context.Context, string) error { return nil }

// --- assemble --------------------------------------------------------------

func assembleProbeOps(t *testing.T) map[string]speedProbeResult {
	t.Helper()
	asm, err := assemble.New(assemble.Deps{
		Tokens: assemble.CounterFunc(func(s string) int { return len(s)/4 + 1 }),
		Scrub:  func(s string) string { return s },
	})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]speedProbeResult{}
	core2 := measureInproc(func() error { _ = assemble.Stable(assemble.StableOptions{}); return nil })
	core2.Source = "assemble.Stable (system prompt tier)"
	out["CORE-002"] = core2

	pi := newProbeIntegration(t)
	core3 := measureInproc(func() error { _ = pi.all.Schemas(); return nil })
	core3.Source = "tools.Registry.Schemas (all wired tools)"
	out["CORE-003"] = core3
	core4 := measureInproc(func() error { _ = pi.file.Schemas(); return nil })
	core4.Source = "tools.Registry.Schemas (file toolset)"
	out["CORE-004"] = core4
	mem26 := measureInproc(func() error {
		f, err := os.CreateTemp(t.TempDir(), "AGENTS-*.md")
		if err != nil {
			return err
		}
		body := strings.Repeat("Project rule: keep changes small.\n", 40)
		if _, err := f.WriteString(body); err != nil {
			_ = f.Close()
			return err
		}
		_ = f.Close()
		_, err = asm.Context(t.Context(), &assemble.ContextInput{
			Files:  []assemble.ContextFile{{Path: f.Name()}},
			Reader: probeReader{},
			Query:  "speed probe",
		})
		return err
	})
	mem26.Source = "assemble.Context (context files tier)"
	out["MEM-026"] = mem26
	skill23 := measureInproc(func() error {
		_, err := asm.Context(t.Context(), &assemble.ContextInput{Retriever: probeRetriever{}, Query: "speed"})
		return err
	})
	skill23.Source = "assemble.Context (skills prompt slot)"
	out["SKILL-023"] = skill23
	return out
}

type probeReader struct{}

func (probeReader) Open(_ context.Context, path string) (io.ReadCloser, error) {
	f, err := os.Open(path) //nolint:gosec // probe reads its own temp fixture
	if err != nil {
		return nil, fmt.Errorf("probe read %s: %w", path, err)
	}
	return f, nil
}

type probeRetriever struct{}

func (probeRetriever) Memories(context.Context, string) ([]assemble.Memory, error) {
	return []assemble.Memory{{Text: "prefer small diffs", Pinned: true}}, nil
}
func (probeRetriever) Skills(context.Context, string) ([]string, error) {
	return []string{"arxiv: papers", "obsidian: notes", "codebase-inspection: repo"}, nil
}

// --- slash -----------------------------------------------------------------

type probeTicket struct{}

func slashProbeOps() map[string]speedProbeResult {
	cmds := gateway.NewCommands(&gateway.CommandOptions{
		BotUsername: "speedbot",
		Runtime:     probeRuntime{},
		Settings:    probeSettings{},
		Control:     probeControl{},
		F8:          probeF8{},
		Kanban:      probeKanban{},
	})
	ticket := &gateway.Ticket{
		Identity: gateway.Identity{Platform: "telegram", ExternalID: "speed", Role: gateway.RoleOwner,
			Status: gateway.StatusAllowlisted, Strength: gateway.StrengthCryptographic},
		Scope: gateway.Scope{Platform: "telegram", ChatID: "speed", UserID: "speed"},
	}
	ops := map[string]string{
		"SLASH-001": "/start",
		"SLASH-002": "/new",
		"SLASH-028": "/queue list",
		"SLASH-040": "/context",
		"SLASH-041": "/whoami",
		"SLASH-049": "/personality",
		"SLASH-052": "/timestamps",
		"SLASH-056": "/footer",
		"SLASH-061": "/theme",
		"SLASH-076": "/cron list",
		"SLASH-079": "/curator status",
		"SLASH-080": "/kanban list",
		"SLASH-090": "/usage",
		"SLASH-094": "/insights",
		"SLASH-095": "/platforms",
	}
	out := map[string]speedProbeResult{}
	for id, text := range ops {
		if id == "SLASH-028" || id == "SLASH-040" || id == "SLASH-061" {
			continue // measured by the internal/tui probe
		}
		res := measureInproc(func() error {
			r := cmds.Dispatch(context.Background(), ticket, text)
			if !r.Handled {
				return fmt.Errorf("%s not handled", text)
			}
			if strings.Contains(r.Reply, "not supported") || strings.Contains(r.Reply, "module_unavailable") {
				return errors.New(r.Reply)
			}
			return nil
		})
		res.Source = "internal/gateway dispatch: " + text
		out[id] = res
	}
	return out
}

type probeRuntime struct{}

func (probeRuntime) StopTurn(context.Context, *gateway.Scope) (bool, error) { return true, nil }
func (probeRuntime) NewSession(_ context.Context, s *gateway.Scope) (string, error) {
	return s.ChatID + "-new", nil
}
func (probeRuntime) Status(context.Context) (gateway.RuntimeStatus, error) {
	return gateway.RuntimeStatus{Version: "speed", Uptime: time.Second}, nil
}
func (probeRuntime) Usage(context.Context, *gateway.Scope) (gateway.UsageReport, bool, error) {
	return gateway.UsageReport{}, false, nil
}
func (probeRuntime) TopicSession(_ context.Context, s *gateway.Scope) (string, error) {
	return s.ChatID + "-topic", nil
}
func (probeRuntime) CronList(context.Context) ([]cron.Job, error) {
	return []cron.Job{{ID: "probe", Name: "probe", Schedule: "0 * * * *", Enabled: true}}, nil
}
func (probeRuntime) CronSetEnabled(context.Context, string, bool, string) error { return nil }
func (probeRuntime) CronRemove(context.Context, string) error                   { return nil }
func (probeRuntime) CronRun(context.Context, string) error                      { return nil }

type probeSettings struct{}

func (probeSettings) ChatSetting(context.Context, string, string, string) (string, bool, error) {
	return "", false, nil
}
func (probeSettings) SetChatSetting(context.Context, string, string, string, string, int64) error {
	return nil
}
func (probeSettings) HomeChannel(context.Context) (string, string, string, bool, error) {
	return "", "", "", false, nil
}
func (probeSettings) SetHomeChannel(context.Context, string, string, string, int64) error { return nil }

type probeControl struct{}

func (probeControl) Restart(context.Context, *gateway.Scope) (gateway.RestartReport, error) {
	return gateway.RestartReport{}, nil
}
func (probeControl) Platforms(context.Context) ([]gateway.AdapterStatus, error) {
	return []gateway.AdapterStatus{{Key: gateway.Key{Platform: "telegram", Account: "speed"}, State: gateway.StateRunning}}, nil
}
func (probeControl) PausePlatform(_ context.Context, name string) (gateway.AdapterStatus, error) {
	return gateway.AdapterStatus{Key: gateway.Key{Platform: name, Account: "speed"}, State: gateway.StatePaused}, nil
}
func (probeControl) ResumePlatform(_ context.Context, name string) (gateway.AdapterStatus, error) {
	return gateway.AdapterStatus{Key: gateway.Key{Platform: name, Account: "speed"}, State: gateway.StateRunning}, nil
}

type probeF8 struct{}

func (probeF8) F8Gateway(context.Context, gateway.Role, string, string, []string) string {
	return "ok"
}

type probeKanban struct{}

func (probeKanban) Kanban(context.Context, gateway.Role, string, []string) string {
	return "board: empty"
}

// sha256Sum keeps probeSpill content-addressed without importing crypto in
// the test body.
func sha256Sum(b []byte) [32]byte {
	return sha256.Sum256(b)
}

type probeNetBackend struct {
	exec.Backend
	id string
}

func (b probeNetBackend) ID() string {
	if b.id != "" {
		return b.id
	}
	return b.Backend.ID()
}

func (b probeNetBackend) Caps() exec.Caps {
	c := b.Backend.Caps()
	c.Network = true // the probe measures the code runner path, not isolation
	return c
}

type probeApprover struct{}

func (probeApprover) Approve(context.Context, *tools.ApprovalRequest) (tools.Approval, error) {
	return tools.ApprovalAllowOnce, nil
}
