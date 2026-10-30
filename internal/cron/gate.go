package cron

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// MaxGateBytes bounds cron_jobs.gate (COST-13): a gate is a declaration, not
// a script body.
const MaxGateBytes = 2000

// ErrGateInvalid rejects a gate document that is malformed or names more than
// one source.
var ErrGateInvalid = errors.New("cron: invalid gate")

// Gate is the parsed cron_jobs.gate JSON: verdict, script or monitor.
type Gate struct {
	Wake    *bool    `json:"wake,omitempty"`
	Context string   `json:"context,omitempty"`
	Script  []string `json:"script,omitempty"`
	Monitor *Monitor `json:"monitor,omitempty"`
	Timeout int      `json:"timeout_s,omitempty"`
}

// Monitor hashes normalized page text; an unchanged hash wakes nothing.
type Monitor struct {
	URL      string   `json:"url"`
	Selector string   `json:"selector,omitempty"`
	Ignore   []string `json:"ignore,omitempty"`
	Context  string   `json:"context,omitempty"`
}

// ParseGate decodes and validates one gate document. Empty means no gate.
func ParseGate(raw string) (*Gate, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > MaxGateBytes {
		return nil, fmt.Errorf("%w: %d bytes > %d", ErrGateInvalid, len(raw), MaxGateBytes)
	}
	var g Gate
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGateInvalid, err)
	}
	n := 0
	if g.Wake != nil {
		n++
	}
	if len(g.Script) > 0 {
		n++
	}
	if g.Monitor != nil {
		n++
	}
	if n != 1 {
		return nil, fmt.Errorf("%w: exactly one of wake, script or monitor is required", ErrGateInvalid)
	}
	if g.Monitor != nil {
		if g.Monitor.URL == "" {
			return nil, fmt.Errorf("%w: monitor.url is required", ErrGateInvalid)
		}
		for _, pat := range g.Monitor.Ignore {
			if _, err := regexp.Compile(pat); err != nil {
				return nil, fmt.Errorf("%w: monitor.ignore %q: %w", ErrGateInvalid, pat, err)
			}
		}
	}
	return &g, nil
}

// GateResult is the verdict of one gate evaluation.
type GateResult struct {
	Wake    bool
	Reason  string
	Context string
}

// GateStore persists the last normalized monitor hash per job. It uses the
// existing module_state key/value table, so no migration is needed.
type GateStore interface {
	GateHash(ctx context.Context, jobID string) (string, bool, error)
	SetGateHash(ctx context.Context, jobID, hash string) error
}

// GateRunner evaluates Job.Gate before the executor runs (COST-13).
type GateRunner interface {
	Check(ctx context.Context, j *Job, g *Gate) (GateResult, error)
}

// Gates is the built-in gate runner; a nil seam is a configuration error.
type Gates struct {
	Runner  *exec.Runner
	Fetch   func(ctx context.Context, url string) (string, error)
	Store   GateStore
	Timeout time.Duration
}

// Check evaluates g for j.
func (g *Gates) Check(ctx context.Context, j *Job, gate *Gate) (GateResult, error) {
	switch {
	case gate.Wake != nil:
		return GateResult{Wake: *gate.Wake, Reason: "verdict", Context: gate.Context}, nil
	case len(gate.Script) > 0:
		return g.script(ctx, j, gate)
	case gate.Monitor != nil:
		return g.monitor(ctx, j, gate)
	}
	return GateResult{}, fmt.Errorf("%w: empty", ErrGateInvalid)
}
func (g *Gates) script(ctx context.Context, j *Job, gate *Gate) (GateResult, error) {
	if g.Runner == nil {
		return GateResult{}, errors.New("cron: gate script runner is not wired")
	}
	spec, _, err := exec.Prepare(&exec.OneShot{
		Direct: gate.Script, Dir: j.Cwd, Root: j.Cwd,
		Timeout: g.timeout(gate), Parent: os.Environ(), Policy: &exec.EnvPolicy{},
	})
	if err != nil {
		return GateResult{}, fmt.Errorf("cron: prepare gate: %w", err)
	}
	res, err := g.Runner.Run(ctx, &spec)
	if err != nil {
		return GateResult{}, fmt.Errorf("cron: run gate: %w", err)
	}
	if res.Status != exec.StatusExited || res.ExitCode == nil {
		return GateResult{}, fmt.Errorf("cron: gate status %s", res.Status)
	}
	switch *res.ExitCode {
	case 0:
		return GateResult{Wake: true, Reason: "script exit 0", Context: gate.Context}, nil
	case 1:
		return GateResult{Wake: false, Reason: "script exit 1"}, nil
	default:
		return GateResult{}, fmt.Errorf("cron: gate exit %d", *res.ExitCode)
	}
}
func (g *Gates) monitor(ctx context.Context, j *Job, gate *Gate) (GateResult, error) {
	m := gate.Monitor
	if g.Fetch == nil {
		return GateResult{}, errors.New("cron: monitor fetch is not wired")
	}
	if g.Store == nil {
		return GateResult{}, errors.New("cron: monitor gate has no hash store")
	}
	body, err := g.Fetch(ctx, m.URL)
	if err != nil {
		return GateResult{}, fmt.Errorf("cron: monitor fetch: %w", err)
	}
	text, err := normalizeMonitor(body, m.Selector, m.Ignore)
	if err != nil {
		return GateResult{}, fmt.Errorf("cron: monitor %s: %w", m.URL, err)
	}
	sum := sha256.Sum256([]byte(text))
	hash := hex.EncodeToString(sum[:])
	prev, found, err := g.Store.GateHash(ctx, j.ID)
	if err != nil {
		return GateResult{}, fmt.Errorf("cron: gate hash: %w", err)
	}
	if found && prev == hash {
		return GateResult{Wake: false, Reason: "monitor unchanged " + hash[:12]}, nil
	}
	if err := g.Store.SetGateHash(ctx, j.ID, hash); err != nil {
		return GateResult{}, fmt.Errorf("cron: save gate hash: %w", err)
	}
	gctx := m.Context
	if gctx == "" {
		gctx = gate.Context
	}
	return GateResult{Wake: true, Reason: "monitor changed " + hash[:12], Context: gctx}, nil
}
func (g *Gates) timeout(gate *Gate) time.Duration {
	if gate.Timeout > 0 {
		return min(time.Duration(gate.Timeout)*time.Second, exec.MaxTimeout)
	}
	if g.Timeout > 0 {
		return min(g.Timeout, exec.MaxTimeout)
	}
	return 30 * time.Second
}

// normalizeMonitor extracts selected text, applies ignores, collapses space.
func normalizeMonitor(body, selector string, ignore []string) (string, error) {
	text := stripScripts(body)
	if selector != "" {
		text = selectOne(text, selector)
	}
	out := strings.Join(strings.Fields(html.UnescapeString(stripTags(text))), " ")
	for _, pat := range ignore {
		re, cerr := regexp.Compile(pat)
		if cerr != nil {
			return "", fmt.Errorf("ignore %q: %w", pat, cerr)
		}
		out = re.ReplaceAllString(out, " ")
	}
	return strings.Join(strings.Fields(out), " "), nil
}

var (
	tagRE     = regexp.MustCompile(`(?is)<([a-zA-Z][\w:-]*)([^>]*)>`)
	scriptRE  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	styleRE   = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	commentRE = regexp.MustCompile(`(?s)<!--.*?-->`)
)

func stripScripts(s string) string {
	s = scriptRE.ReplaceAllString(s, " ")
	s = styleRE.ReplaceAllString(s, " ")
	return commentRE.ReplaceAllString(s, " ")
}

// selectOne returns the inner HTML of the first element matching sel.
func selectOne(doc, sel string) string {
	tag := "*"
	if i := strings.IndexAny(sel, ".#"); i >= 0 {
		if i > 0 {
			tag = strings.ToLower(sel[:i])
		}
		sel = sel[i:]
	}
	for _, m := range tagRE.FindAllStringSubmatchIndex(doc, -1) {
		name := strings.ToLower(doc[m[2]:m[3]])
		if tag != "*" && name != tag || !selectorMatches(doc[m[4]:m[5]], sel) {
			continue
		}
		rest := doc[m[1]:]
		if end := strings.Index(strings.ToLower(rest), "</"+name); end >= 0 {
			return rest[:end]
		}
		return rest
	}
	return ""
}
func selectorMatches(raw, sel string) bool {
	if sel == "" {
		return true
	}
	kind, val := sel[0], sel[1:]
	attr := "id"
	if kind == '.' {
		attr = "class"
	}
	m := regexp.MustCompile(`(?is)\b` + attr + `\s*=\s*["']?([^"'\s>]+)`).FindStringSubmatch(raw)
	if m == nil {
		return false
	}
	if kind == '#' {
		return m[1] == val
	}
	for _, c := range strings.Fields(m[1]) {
		if c == val {
			return true
		}
	}
	return false
}

// stripTags keeps text nodes only.
func stripTags(s string) string { return tagRE.ReplaceAllString(s, " ") }
