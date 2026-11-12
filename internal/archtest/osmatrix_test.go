package archtest

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// f9OSTargets = 8 target rilis kanonik (ENGINEERING E-39, CROSS-CUTTING §7).
func f9OSTargets() []string {
	return []string{
		"linux/amd64", "linux/arm64", "linux/arm",
		"darwin/amd64", "darwin/arm64",
		"windows/amd64", "windows/arm64",
		"android/arm64",
	}
}

// f9QEMUTargets = dua target E-39 tanpa runner hosted (ROADMAP F9, ADR-073).
func f9QEMUTargets() []string { return []string{"linux/arm", "android/arm64"} }

var osArchRe = regexp.MustCompile(`^(amd64|arm64|arm)\b`)

// TestF9OSMatrix (ROADMAP F9 deliverable, CROSS-CUTTING §7): matriks OS F9
// mencakup 8 target rilis kanonik, `make build-matrix` membangun semuanya, dan
// dua target tanpa runner hosted benar-benar MENJALANKAN suite inti di QEMU
// (bukan hanya cross-compile). Selama wiring QEMU belum ada gate ini skip; dengan
// DAMNBOT_RELEASE=1 prasyarat yang hilang menggagalkan gate (ADR-347).
func TestF9OSMatrix(t *testing.T) {
	t.Parallel()
	want := f9OSTargets()
	if targets := makefileTargets(t); !slices.Equal(targets, want) {
		t.Fatalf("TARGETS = %v, want %v (E-39)", targets, want)
	}
	docs := crossCuttingTargets(t)
	wantSorted := slices.Clone(want)
	slices.Sort(docs)
	slices.Sort(wantSorted)
	if !slices.Equal(docs, wantSorted) {
		t.Fatalf("CROSS-CUTTING §7 = %v, want %v", docs, wantSorted)
	}

	ciRaw := readRepoFile(t, ".github/workflows/ci.yml")
	ci := parseWorkflow(t, ".github/workflows/ci.yml")
	var missing []string
	// Bukti build: satu target Makefile untuk delapan target, dijalankan job
	// `portability` di tier full dan `make build-matrix` lokal.
	mk := readRepoFile(t, "Makefile")
	if !strings.Contains(mk, "\nbuild-matrix:") {
		missing = append(missing, "Makefile tidak punya target build-matrix")
	}
	if !jobRuns(ci.Jobs["portability"], "make build-matrix") {
		missing = append(missing, "job portability tidak menjalankan make build-matrix")
	}
	// Bukti cakupan target engine: matrix sqlite-engine tetap delapan entri E-39.
	for _, target := range want {
		if !strings.Contains(ciRaw, `"target":"`+target+`"`) {
			missing = append(missing, "job sqlite-engine tidak memuat target "+target)
		}
	}
	for _, os := range []string{"ubuntu-latest", "ubuntu-24.04-arm", "macos-latest", "macos-15-intel", "windows-latest", "windows-11-arm"} {
		if !strings.Contains(ciRaw, `"os":"`+os+`"`) {
			missing = append(missing, "matrix test penuh tidak memuat runner "+os)
		}
	}

	// Bukti QEMU: job nyata yang mengompilasi lalu MENJALANKAN test binary inti
	// di userspace arch target (linux/arm + android/arm64).
	missing = append(missing, qemuWiringProblems(ci, readRepoFile(t, "scripts/os-matrix-qemu.sh"))...)
	if len(missing) > 0 {
		skipOrFail(t, strings.Join(missing, "; "))
	}
}

// TestNightlyOSMatrixWired (ROADMAP F9, ADR-135 butir 3): OS matrix QEMU ikut
// nightly. Nightly memanggil `ci` lewat workflow_call; job `os-matrix-qemu`
// adalah tier `full`, sehingga hilangnya job/wiring harus menggagalkan gate ini
// (bukan skip) supaya regresi wiring tidak lolos diam-diam.
func TestNightlyOSMatrixWired(t *testing.T) {
	t.Parallel()
	nightly := parseWorkflow(t, ".github/workflows/nightly.yml")
	full, ok := nightly.Jobs["ci-full"]
	if !ok || full.Uses != "./.github/workflows/ci.yml" {
		t.Fatal("nightly.yml: job ci-full tidak memanggil ./.github/workflows/ci.yml")
	}
	ci := parseWorkflow(t, ".github/workflows/ci.yml")
	job, ok := ci.Jobs["os-matrix-qemu"]
	if !ok {
		t.Fatal("ci.yml: job os-matrix-qemu tidak ada; nightly tidak menjalankan OS matrix QEMU")
	}
	if !strings.Contains(job.If, "needs.changes.outputs.tier == 'full'") {
		t.Fatalf("os-matrix-qemu.if = %q, want tier == 'full' (workflow_call nightly masuk tier full)", job.If)
	}
	if !strings.Contains(readRepoFile(t, "scripts/ci-required.sh"), "os-matrix-qemu") {
		t.Fatal("scripts/ci-required.sh: gate os-matrix-qemu tidak wajib di tier full")
	}
}

// qemuWiringProblems memeriksa job `os-matrix-qemu` dan script-nya. Semua
// kegagalan dikumpulkan agar satu run menampilkan seluruh wiring yang hilang.
func qemuWiringProblems(ci wfDoc, script string) []string {
	var out []string
	job, ok := ci.Jobs["os-matrix-qemu"]
	if !ok {
		return []string{"ci.yml: job os-matrix-qemu tidak ada (linux/arm + android/arm64 QEMU)"}
	}
	if !strings.Contains(job.If, "needs.changes.outputs.tier == 'full'") {
		out = append(out, "os-matrix-qemu.if tidak membatasi tier full (nightly/workflow_call tidak menjalankannya)")
	}
	got := jobMatrixTargets(job)
	for _, target := range f9QEMUTargets() {
		if !slices.Contains(got, target) {
			out = append(out, "matrix os-matrix-qemu tidak memuat "+target)
		}
	}
	var runs strings.Builder
	for _, step := range job.Steps {
		runs.WriteString(step.Run)
		runs.WriteByte('\n')
	}
	all := runs.String()
	for _, want := range []string{
		"docker run", "--platform", "os-matrix-qemu.sh build", "os-matrix-qemu.sh run",
	} {
		if !strings.Contains(all, want) {
			out = append(out, "step os-matrix-qemu tidak memuat "+want)
		}
	}
	var qemuAction bool
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "docker/setup-qemu-action@") {
			qemuAction = true
			platforms, isString := step.With["platforms"].(string)
			if !isString || !strings.Contains(platforms, "arm") {
				out = append(out, "docker/setup-qemu-action tanpa platforms arm/arm64")
			}
		}
	}
	if !qemuAction {
		out = append(out, "docker/setup-qemu-action tidak dipin di job os-matrix-qemu")
	}
	// Script harus menjalankan (bukan hanya mengompilasi) paket inti di arch target.
	for _, want := range []string{
		"go test", "-c -o", "-tags", "-test.count=1", "./internal/ctxdb/...", "./internal/exec/...",
		"./internal/acp/...", "./cmd/damnbot", "^TestRunHeadless",
	} {
		if !strings.Contains(script, want) {
			out = append(out, "scripts/os-matrix-qemu.sh tidak memuat "+want)
		}
	}
	return out
}

// makefileTargets membaca daftar `TARGETS := …` (E-39).
func makefileTargets(t *testing.T) []string {
	t.Helper()
	for line := range strings.Lines(readRepoFile(t, "Makefile")) {
		rest, ok := strings.CutPrefix(line, "TARGETS := ")
		if ok {
			return strings.Fields(rest)
		}
	}
	t.Fatal("Makefile: TARGETS tidak ditemukan")
	return nil
}

// crossCuttingTargets menyusun goos/arch dari tabel CROSS-CUTTING §7.
func crossCuttingTargets(t *testing.T) []string {
	t.Helper()
	var out []string
	section := sectionText(t, readRepoFile(t, "docs/CROSS-CUTTING.md"), "## 7. Lintas platform", "\n## ")
	for line := range strings.Lines(section) {
		cells := tableCells(line)
		if len(cells) < 2 {
			continue
		}
		fields := strings.Fields(cells[0])
		if len(fields) == 0 {
			continue
		}
		goos := map[string]string{
			"Linux": "linux", "macOS": "darwin", "Windows": "windows", "Android": "android",
		}[fields[0]]
		if goos == "" {
			continue
		}
		for arch := range strings.SplitSeq(cells[1], ",") {
			arch = strings.TrimSpace(arch)
			m := osArchRe.FindStringSubmatch(arch)
			if m == nil {
				continue
			}
			out = append(out, goos+"/"+m[1])
		}
	}
	return out
}

// wfDoc/wfJob/wfStep = bentuk minimal workflow GitHub Actions yang dibaca gate
// (job matrix statis + step). Matrix dinamis `fromJSON(...)` sengaja dibiarkan
// sebagai string; gate QEMU membaca matrix statis job sendiri.
type wfDoc struct {
	Jobs map[string]wfJob `yaml:"jobs"`
}

type wfJob struct {
	If       string `yaml:"if"`
	Uses     string `yaml:"uses"`
	Strategy struct {
		Matrix any `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []wfStep `yaml:"steps"`
}

type wfStep struct {
	Name string         `yaml:"name"`
	Run  string         `yaml:"run"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

// parseWorkflow mem-parse YAML workflow; YAML tidak valid = gagal keras.
func parseWorkflow(t *testing.T, path string) wfDoc {
	t.Helper()
	var doc wfDoc
	if err := yaml.Unmarshal([]byte(readRepoFile(t, path)), &doc); err != nil {
		t.Fatalf("%s: YAML tidak valid: %v", path, err)
	}
	if len(doc.Jobs) == 0 {
		t.Fatalf("%s: tidak ada job", path)
	}
	return doc
}

// jobRuns reports whether any step of job executes the given command text.
func jobRuns(job wfJob, want string) bool {
	for _, step := range job.Steps {
		if strings.Contains(step.Run, want) {
			return true
		}
	}
	return false
}

// jobMatrixTargets mengembalikan field `target` dari strategy.matrix.include.
func jobMatrixTargets(job wfJob) []string {
	matrix, ok := job.Strategy.Matrix.(map[string]any)
	if !ok {
		return nil
	}
	include, ok := matrix["include"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, entry := range include {
		row, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if target, ok := row["target"].(string); ok {
			out = append(out, target)
		}
	}
	return out
}
