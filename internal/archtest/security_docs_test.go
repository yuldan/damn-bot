package archtest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// securityEmailRe matches the reporter mailbox recorded in SECURITY.md.
var securityEmailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// docFailures collects every missing policy element before failing once, so a
// reporter sees the whole gap list instead of one line at a time.
type docFailures []string

func (d *docFailures) add(format string, args ...any) {
	*d = append(*d, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (d *docFailures) report(t *testing.T, title string) {
	t.Helper()
	if len(*d) > 0 {
		t.Fatalf("%s:\n- %s", title, strings.Join(*d, "\n- "))
	}
}

// TestSecurityPolicySLA (GAP-SEC-28, ADR-347) pins the parts of SECURITY.md a
// reporter relies on: the N/N-1 support table, the three SLA numbers, the
// private GHSA + PGP email channels, and the CVE steps. A changed number or a
// dropped channel fails the test instead of silently weakening the policy.
func TestSecurityPolicySLA(t *testing.T) {
	t.Parallel()
	doc := readRepoFile(t, "SECURITY.md")
	var fail docFailures
	checkSecurityVersions(t, doc, fail.add)
	checkSecuritySLA(t, doc, fail.add)
	checkSecurityChannels(doc, fail.add)
	checkSecurityCVE(t, doc, fail.add)
	fail.report(t, "kebijakan keamanan tidak lengkap")
}

// TestTrustModelDoc (GAP-SEC-28, ADR-347) pins the mandatory TRUST-MODEL
// contents from SPEC §22.1: every actor row, the four residual IDs, backup
// retention, restore-to-another-data-dir, the boundary statements, and the
// explicit threat model/data-flow/secret sections.
func TestTrustModelDoc(t *testing.T) {
	t.Parallel()
	doc := readRepoFile(t, "docs/TRUST-MODEL.md")
	var fail docFailures
	checkTrustActors(t, doc, fail.add)
	checkTrustResiduals(t, doc, fail.add)
	checkTrustBoundaries(doc, fail.add)
	checkTrustThreat(t, doc, fail.add)
	checkTrustFlow(t, doc, fail.add)
	fail.report(t, "TRUST-MODEL tidak lengkap sesuai SPEC §22.1")
}

func checkSecurityVersions(t *testing.T, doc string, add func(string, ...any)) {
	t.Helper()
	versions := securityTable(t, doc, "Versi")
	for _, want := range []string{"N", "N-1"} {
		cell, ok := findVersionRow(versions, want)
		if !ok {
			add("SECURITY.md: tabel versi tidak memuat baris %q (N dan N-1 minor wajib)", want)
			continue
		}
		if len(cell) < 2 || strings.TrimSpace(cell[1]) == "" || strings.Contains(strings.ToLower(cell[1]), "tidak") {
			add("SECURITY.md: baris versi %q tidak menyatakan dukungan: %v", want, cell)
		}
	}
}

func checkSecuritySLA(t *testing.T, doc string, add func(string, ...any)) {
	t.Helper()
	sla := securityTable(t, doc, "Tahap")
	for _, want := range []struct{ stage, target string }{
		{"ack", "72 jam"},
		{"triage", "7 hari"},
		{"fix critical", "30 hari"},
	} {
		cell, ok := findRow(sla, want.stage)
		if !ok {
			add("SECURITY.md: baris SLA %q hilang", want.stage)
			continue
		}
		if len(cell) < 2 || !strings.Contains(strings.ToLower(cell[1]), want.target) {
			add("SECURITY.md: SLA %q = %v, wajib memuat %q", want.stage, cell, want.target)
		}
	}
}

func checkSecurityChannels(doc string, add func(string, ...any)) {
	for _, want := range []string{
		"https://github.com/yuldan/damn-bot/security/advisories/new",
		"GHSA",
		"Security Advisory",
		"privat",
		"PGP",
		"https://github.com/yuldan.gpg",
	} {
		if !strings.Contains(doc, want) {
			add("SECURITY.md: kanal privat wajib memuat %q", want)
		}
	}
	if !securityEmailRe.MatchString(doc) {
		add("SECURITY.md: tidak ada alamat email pelaporan")
	}
}

func checkSecurityCVE(t *testing.T, doc string, add func(string, ...any)) {
	t.Helper()
	cve := sectionBetween(t, doc, "## 4. Proses CVE", "## 5.")
	for _, want := range []string{"CVE ID", "CNA", "GHSA", "90 hari"} {
		if !strings.Contains(cve, want) {
			add("SECURITY.md §4: proses CVE wajib memuat %q", want)
		}
	}
	if n := regexp.MustCompile(`(?m)^[0-9]+\. `).FindAllString(cve, -1); len(n) < 4 {
		add("SECURITY.md §4: proses CVE hanya %d langkah, minimal 4", len(n))
	}
}

func checkTrustActors(t *testing.T, doc string, add func(string, ...any)) {
	t.Helper()
	actors := securityTable(t, doc, "Aktor")
	for _, want := range []string{
		"User OS lokal", "Owner", "User gateway", "Kanal", "Klien mcp serve",
		"Server MCP", "Skill", "Model (output LLM)", "Konten eksternal",
		"Konten repo/workspace", "Proses damnbot lain",
	} {
		if _, ok := findRow(actors, want); !ok {
			add("TRUST-MODEL §2: aktor %q hilang dari tabel", want)
		}
	}
}

func checkTrustResiduals(t *testing.T, doc string, add func(string, ...any)) {
	t.Helper()
	residuals := sectionBetween(t, doc, "## 3. Residu", "## 4.")
	for _, id := range []string{"#37", "#40", "#48", "#68"} {
		if !strings.Contains(residuals, id) {
			add("TRUST-MODEL §3: residu %s hilang", id)
		}
	}
	checkBackupRetention(t, residuals, add)
	if !strings.Contains(residuals, "data dir lain") || !strings.Contains(residuals, "purge.log") {
		add("TRUST-MODEL §3: residu restore ke data dir lain (tanpa purge.log) hilang")
	}
}

func checkBackupRetention(t *testing.T, residuals string, add func(string, ...any)) {
	t.Helper()
	cell, ok := findRow(securityTable(t, residuals, "Residu"), "Backup otomatis")
	if !ok {
		add("TRUST-MODEL §3: baris retensi backup (Backup otomatis) hilang")
		return
	}
	row := strings.Join(cell, " | ")
	for _, want := range []string{"daily-*", "premigrate-*", "3", "2"} {
		if !strings.Contains(row, want) {
			add("TRUST-MODEL §3: retensi backup wajib memuat %q: %s", want, row)
		}
	}
}

func checkTrustBoundaries(doc string, add func(string, ...any)) {
	plain := strings.NewReplacer("**", "", "`", "").Replace(doc)
	for _, want := range []string{
		"hanya untuk tool kode",
		"shell bukan boundary path",
		"internal/netx",
		"P25",
		"(a)", "(b)", "(c)", "(d)",
	} {
		if !strings.Contains(plain, want) {
			add("TRUST-MODEL §1/§5: batas keamanan wajib memuat %q", want)
		}
	}
}

func checkTrustThreat(t *testing.T, doc string, add func(string, ...any)) {
	t.Helper()
	threat := sectionBetween(t, doc, "## 4. Threat model", "## 5.")
	for _, want := range []string{"Penyerang", "Prompt injection", "Supply chain", "Di luar cakupan", "Asumsi"} {
		if !strings.Contains(threat, want) {
			add("TRUST-MODEL §4: threat model wajib memuat %q", want)
		}
	}
}

func checkTrustFlow(t *testing.T, doc string, add func(string, ...any)) {
	t.Helper()
	flow := sectionBetween(t, doc, "## 5. Alur data", "")
	for _, want := range []string{
		"Input kanal", "Assemble", "Dispatch tool", "Egress", "Penyimpanan",
		"Purge/restore", "Sandbox", "Secret", "keychain", "redaksi",
	} {
		if !strings.Contains(flow, want) {
			add("TRUST-MODEL §5: alur data/sandbox/secret wajib memuat %q", want)
		}
	}
}

// securityTable returns the body rows of the first Markdown table whose header
// contains head as a cell.
func securityTable(t *testing.T, doc, head string) [][]string {
	t.Helper()
	var rows [][]string
	inTable := false
	for line := range strings.Lines(doc) {
		cells := tableCells(line)
		switch {
		case len(cells) == 0:
			inTable = false
		case !inTable:
			inTable = slices.Contains(cells, head)
		case strings.HasPrefix(strings.TrimSpace(cells[0]), "---"):
		default:
			rows = append(rows, cells)
		}
	}
	if len(rows) == 0 {
		t.Fatalf("tabel dengan header %q tidak ditemukan", head)
	}
	return rows
}

// findVersionRow matches a support-table row by the version label before any
// parenthetical, so the N row cannot satisfy the N-1 requirement.
func findVersionRow(rows [][]string, want string) ([]string, bool) {
	want = normalizeCell(want)
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		label := normalizeCell(row[0])
		if i := strings.Index(label, "("); i >= 0 {
			label = strings.TrimSpace(label[:i])
		}
		if label == want {
			return row, true
		}
	}
	return nil, false
}

// findRow matches a table row whose first cell starts with want after
// normalizing Markdown emphasis, backticks, extra spaces, and case.
func findRow(rows [][]string, want string) ([]string, bool) {
	want = normalizeCell(want)
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		if strings.HasPrefix(normalizeCell(row[0]), want) {
			return row, true
		}
	}
	return nil, false
}

func normalizeCell(cell string) string {
	cell = strings.NewReplacer("**", "", "`", "", "−", "-", "—", "-").Replace(cell)
	return strings.ToLower(strings.Join(strings.Fields(cell), " "))
}

// sectionBetween returns doc from start up to end; an empty end means EOF.
func sectionBetween(t *testing.T, doc, start, end string) string {
	t.Helper()
	i := strings.Index(doc, start)
	if i < 0 {
		t.Fatalf("bagian %q tidak ditemukan", start)
	}
	body := doc[i+len(start):]
	if end != "" {
		if j := strings.Index(body, end); j >= 0 {
			body = body[:j]
		}
	}
	return body
}
