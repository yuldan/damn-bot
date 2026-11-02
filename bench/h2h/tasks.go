package h2h

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/bench/evals"
)

// SmokeQuota adalah komposisi smoke F0 (10 task, subset E1–E7). Dipilih hanya task yang
// self-contained di workstation: E1 internal (bukan SWE-bench/docker) dengan toolchain
// tersedia, E2 seluruhnya, E3 (server halaman lokal), E6 dengan fault HTTP. E4/E5 butuh
// sesi multi-turn/lintas sesi, E6 file_locked butuh kunci wajib yang tidak ada di Linux,
// E7 butuh docker Terminal-Bench — dilewati di smoke ini.
var SmokeQuota = []struct {
	Set string
	N   int
}{{"E1", 4}, {"E2", 2}, {"E3", 2}, {"E6", 2}}

// SelectSalt membuat urutan pilihan stabil dan terdokumentasi.
const SelectSalt = "h2h-smoke/v1|"

// F5SmokeSalt is the F5 E1 smoke selection (ROADMAP F5, ADR-032): ten
// self-contained E1 tasks; no cross-agent gate in F5.
const F5SmokeSalt = "h2h-smoke/v1|E1|"

// F5CheapModel is the default cheap model of the F5 smoke (override with
// -model; prices still come from the explicit -price-* flags).
const F5CheapModel = "openai/gpt-4o-mini"

// LoadSet membaca <dir>/<set>.jsonl.
func LoadSet(dir, set string) ([]evals.Task, error) {
	f, err := os.Open(filepath.Join(dir, set+".jsonl"))
	if err != nil {
		return nil, fmt.Errorf("h2h: %w", err)
	}
	defer f.Close()
	var out []evals.Task
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var t evals.Task
		if err := json.Unmarshal(sc.Bytes(), &t); err != nil {
			return nil, fmt.Errorf("h2h: %s: %w", set, err)
		}
		out = append(out, t)
	}
	return out, sc.Err()
}

// Eligible melaporkan apakah task bisa dijalankan self-contained di mesin ini.
func Eligible(t *evals.Task, lookPath func(string) (string, error)) bool {
	switch t.Set {
	case "E1":
		if !strings.HasPrefix(t.Kind, "internal-") || t.Grader.Type != "test" {
			return false
		}
		for _, bin := range commandBinaries(t.Grader.Command) {
			if _, err := lookPath(bin); err != nil {
				return false
			}
		}
		return true
	case "E2", "E3":
		return true
	case "E6":
		for _, f := range t.Faults {
			if f.Kind == "file_locked" {
				return false
			}
		}
		return true
	}
	return false
}

// commandBinaries mengambil nama program di posisi perintah (setelah awal, &&, ||, ;, |).
func commandBinaries(cmd string) []string {
	var out []string
	next := true
	for _, tok := range strings.Fields(cmd) {
		switch tok {
		case "&&", "||", ";", "|":
			next = true
			continue
		}
		if next && !strings.HasPrefix(tok, "./") {
			out = append(out, tok)
		}
		next = false
	}
	return out
}

// Select memilih task smoke: per set, task eligible dengan sha256(SelectSalt+id) terkecil.
func Select(dir string, lookPath func(string) (string, error)) ([]evals.Task, error) {
	var out []evals.Task
	for _, q := range SmokeQuota {
		ts, err := LoadSet(dir, q.Set)
		if err != nil {
			return nil, err
		}
		ts = slices.DeleteFunc(ts, func(t evals.Task) bool { return !Eligible(&t, lookPath) })
		slices.SortFunc(ts, func(a, b evals.Task) int { return strings.Compare(selectKey(a.ID), selectKey(b.ID)) })
		if len(ts) < q.N {
			return nil, fmt.Errorf("h2h: %s hanya %d task eligible < %d", q.Set, len(ts), q.N)
		}
		out = append(out, ts[:q.N]...)
	}
	return out, nil
}

func selectKey(id string) string {
	h := sha256.Sum256([]byte(SelectSalt + id))
	return hex.EncodeToString(h[:])
}

// Prompt mengganti {{BASE_URL}} dengan alamat server halaman.
func Prompt(t *evals.Task, baseURL string) string {
	return strings.ReplaceAll(t.Prompt, "{{BASE_URL}}", baseURL)
}

// Materialize menulis fixture task ke dir (dibuat bila belum ada).
func Materialize(t *evals.Task, dir string) error {
	write := func(rel string, b []byte) error {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, b, 0o644)
	}
	for rel, s := range t.Files {
		if err := write(rel, []byte(s)); err != nil {
			return fmt.Errorf("h2h: fixture %s: %w", rel, err)
		}
	}
	for rel, s := range t.FilesB64 {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return fmt.Errorf("h2h: fixture %s: %w", rel, err)
		}
		if err := write(rel, b); err != nil {
			return fmt.Errorf("h2h: fixture %s: %w", rel, err)
		}
	}
	return os.MkdirAll(dir, 0o755)
}

// Verdict adalah hasil grader (informatif di smoke F0).
type Verdict struct {
	Pass   bool     `json:"pass"`
	Detail []string `json:"detail,omitempty"`
}

// ToolCall adalah satu panggilan tool agent (dinormalisasi dari rekaman proxy).
type ToolCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Grade menilai task setelah agent selesai: dir = workdir, answer = jawaban final agent,
// calls = tool call agent (untuk aturan forbidden E6).
func Grade(ctx context.Context, t *evals.Task, dir, answer string, calls []ToolCall) Verdict {
	var v Verdict
	fail := func(format string, a ...any) { v.Detail = append(v.Detail, fmt.Sprintf(format, a...)) }
	g := &t.Grader
	switch g.Type {
	case "test":
		for _, rel := range g.Unchanged {
			got, err := os.ReadFile(filepath.Join(dir, rel))
			if err != nil || string(got) != t.Files[rel] {
				fail("%s berubah atau hilang", rel)
			}
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, "bash", "-c", g.Command)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
		if out, err := cmd.CombinedOutput(); err != nil {
			fail("%s: %v: %s", g.Command, err, tailString(out, 400))
		}
	case "golden":
		for rel, want := range g.Reference {
			if got, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || string(got) != want {
				fail("%s tidak byte-identik dengan referensi", rel)
			}
		}
		for rel, b64 := range g.ReferenceB64 {
			want, _ := base64.StdEncoding.DecodeString(b64)
			if got, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || !bytes.Equal(got, want) {
				fail("%s tidak byte-identik dengan referensi", rel)
			}
		}
	case "lines":
		for _, le := range g.Lines {
			lines := strings.Split(t.Files[le.Path], "\n")
			if le.Line < 1 || le.Line > len(lines) {
				fail("baris %d di luar fixture %s", le.Line, le.Path)
				continue
			}
			lines[le.Line-1] = le.Text
			if got, err := os.ReadFile(filepath.Join(dir, le.Path)); err != nil || string(got) != strings.Join(lines, "\n") {
				fail("%s: baris %d bukan %q atau byte lain berubah", le.Path, le.Line, le.Text)
			}
		}
	case "answer":
		re, err := regexp.Compile(g.Pattern)
		if err != nil {
			fail("pattern: %v", err)
		} else if !re.MatchString(answer) {
			fail("jawaban tidak cocok %s", g.Pattern)
		}
	default:
		fail("grader %q tidak didukung smoke", g.Type)
	}
	for _, rule := range g.Forbidden {
		if hit := forbiddenHit(rule, calls); hit != "" {
			fail("aksi terlarang %s: %s", rule, hit)
		}
	}
	v.Pass = len(v.Detail) == 0
	return v
}

// forbiddenHit menerapkan aturan "exec:<re>" (perintah shell/kode) dan "fs_write:<re>"
// (path tulis) pada tool call; mengembalikan argumen yang melanggar atau "".
func forbiddenHit(rule string, calls []ToolCall) string {
	kind, pat, ok := strings.Cut(rule, ":")
	if !ok {
		return ""
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "pattern invalid: " + err.Error()
	}
	for _, c := range calls {
		var args map[string]any
		_ = json.Unmarshal([]byte(c.Arguments), &args) // argumen rusak = tidak dieksekusi
		var fields []string
		switch kind {
		case "exec":
			if c.Name == "terminal" || c.Name == "execute_code" || c.Name == "process" {
				fields = []string{"command", "code"}
			}
		case "fs_write":
			if c.Name == "write_file" || c.Name == "patch" {
				fields = []string{"path"}
			}
		}
		for _, f := range fields {
			if s, ok := args[f].(string); ok && re.MatchString(s) {
				return c.Name + " " + f + "=" + s
			}
		}
	}
	return ""
}

func tailString(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}

// SelectF5E1 memilih 10 task E1 self-contained untuk smoke F5.
func SelectF5E1(dir string, lookPath func(string) (string, error)) ([]evals.Task, error) {
	ts, err := LoadSet(dir, "E1")
	if err != nil {
		return nil, err
	}
	ts = slices.DeleteFunc(ts, func(t evals.Task) bool { return !Eligible(&t, lookPath) })
	slices.SortFunc(ts, func(a, b evals.Task) int {
		return strings.Compare(selectKeyF5(a.ID), selectKeyF5(b.ID))
	})
	if len(ts) < 10 {
		return nil, fmt.Errorf("h2h: E1 hanya %d task eligible < 10", len(ts))
	}
	return ts[:10], nil
}

func selectKeyF5(id string) string {
	h := sha256.Sum256([]byte(F5SmokeSalt + id))
	return hex.EncodeToString(h[:])
}

// KeyFile resolves the runtime upstream key file: H2H_KEY_FILE, else the
// per-user ~/.config/damnbot/llm_key. It never returns or logs the contents.
func KeyFile(getenv func(string) string, home string) string {
	if v := strings.TrimSpace(getenv("H2H_KEY_FILE")); v != "" {
		return v
	}
	if home == "" {
		return ""
	}
	p := filepath.Join(home, ".config", "damnbot", "llm_key")
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return p
	}
	return ""
}
