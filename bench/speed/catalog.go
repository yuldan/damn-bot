// Package speed adalah kerangka speed parity (SCORECARD §3, ROADMAP F0 #4–#5): setiap baris
// FEATURE-PARITY.md diklasifikasi menjadi operasi terukur (tipe (a), tanpa LLM) atau alasan
// tidak terukur; runner mengukur baseline hermes tanpa jaringan (`unshare -rn`).
package speed

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Row = satu baris FEATURE-PARITY.md.
type Row struct {
	ID      string `json:"id"`
	Prefix  string `json:"prefix"`
	Feature string `json:"feature"`
	Status  string `json:"status"`
}

var rowRe = regexp.MustCompile(`^\| ([A-Z]+)-([0-9]{3}) \|`)

// splitCells memecah baris tabel Markdown pada `|` yang tidak di-escape (`\|`).
func splitCells(line string) []string {
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(line[i])
		}
	}
	return cells[1:] // sel sebelum `|` pertama selalu kosong
}

// ParseParity mengambil semua baris ber-ID dari teks FEATURE-PARITY.md.
func ParseParity(text string) []Row {
	var rows []Row
	for line := range strings.Lines(text) {
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cells := splitCells(strings.TrimRight(line, "\n"))
		rows = append(rows, Row{
			ID: m[1] + "-" + m[2], Prefix: m[1], Feature: cells[1], Status: cells[len(cells)-1],
		})
	}
	return rows
}

// Op = satu operasi hermes yang diukur.
type Op struct {
	Kind    string          `json:"kind"` // cli | tool | slash | py | pty
	Argv    []string        `json:"argv,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Command string          `json:"command,omitempty"`
	Setup   string          `json:"setup,omitempty"`
	Code    string          `json:"code,omitempty"`
	Reset   *Reset          `json:"reset,omitempty"`
	Runs    int             `json:"runs,omitempty"`
	// Proxy: op pengganti `hermes <cmd> --help` (perintah asli mutating/interaktif/jaringan).
	Proxy bool `json:"proxy,omitempty"`
	// Capture: kembalikan output tool run terakhir (Result.Output), mis. untuk token CT-11.
	Capture bool `json:"capture,omitempty"`
	// FreshTask: task_id baru per run (lewati guard pengulangan hermes).
	FreshTask bool `json:"fresh_task,omitempty"`
	// OkExit: exit code non-nol yang tetap dianggap op sukses (mis. doctor = 1 bila ada temuan).
	OkExit []int `json:"ok_exit,omitempty"`
}

// Reset = file yang ditulis ulang sebelum setiap run (di luar waktu ukur).
type Reset struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Catalog = bench/speed/catalog.json.
type Catalog struct {
	PrefixReasons map[string]string `json:"prefix_reasons"`
	Ranges        []struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Reason string `json:"reason"`
	} `json:"ranges"`
	Reasons    map[string]string `json:"reasons"`
	CLIRuns    int               `json:"cli_runs"`
	InprocRuns int               `json:"inproc_runs"`
	Ops        map[string]Op     `json:"ops"`
}

// Kelas entri hasil.
const (
	ClassMeasured      = "measured"
	ClassNotMeasurable = "not_measurable"
	ClassDropped       = "dropped"
	ClassUnclassified  = "unclassified"
)

// Entry = klasifikasi (dan nanti hasil) satu baris parity.
type Entry struct {
	Row
	Class  string  `json:"class"`
	Reason string  `json:"reason,omitempty"`
	Op     *Op     `json:"op,omitempty"`
	Result *Result `json:"result,omitempty"`
}

var cliCmdRe = regexp.MustCompile("^`hermes ([a-z0-9-]+)")

// Classify memetakan setiap baris ke op atau alasan. Urutan: dropped → op eksplisit →
// alasan per ID → rentang → proxy `--help` (CLI) → alasan prefix.
func Classify(rows []Row, cat Catalog) []Entry {
	out := make([]Entry, 0, len(rows))
	for _, r := range rows {
		e := Entry{Row: r}
		switch {
		case strings.HasPrefix(r.Status, "dropped-with-approval"):
			e.Class, e.Reason = ClassDropped, r.Status
		case hasOp(cat, r.ID):
			op := cat.Ops[r.ID]
			e.Class, e.Op = ClassMeasured, &op
		case cat.Reasons[r.ID] != "":
			e.Class, e.Reason = ClassNotMeasurable, cat.Reasons[r.ID]
		case rangeReason(cat, r.ID) != "":
			e.Class, e.Reason = ClassNotMeasurable, rangeReason(cat, r.ID)
		case r.Prefix == "CLI" && cliCmdRe.MatchString(r.Feature):
			cmd := cliCmdRe.FindStringSubmatch(r.Feature)[1]
			e.Class, e.Op = ClassMeasured, &Op{Kind: "cli", Argv: []string{cmd, "--help"}, Proxy: true}
		case cat.PrefixReasons[r.Prefix] != "":
			e.Class, e.Reason = ClassNotMeasurable, cat.PrefixReasons[r.Prefix]
		default:
			e.Class = ClassUnclassified
		}
		out = append(out, e)
	}
	return out
}

func hasOp(cat Catalog, id string) bool { _, ok := cat.Ops[id]; return ok }

// rangeReason: ID dengan prefix sama dan nomor di [From, To] (perbandingan string aman: 3 digit).
func rangeReason(cat Catalog, id string) string {
	for _, rg := range cat.Ranges {
		p, _, _ := strings.Cut(rg.From, "-")
		ip, _, _ := strings.Cut(id, "-")
		if p == ip && id >= rg.From && id <= rg.To {
			return rg.Reason
		}
	}
	return ""
}

// ParseCatalog mem-parse catalog.json.
func ParseCatalog(data []byte) (Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return Catalog{}, fmt.Errorf("speed: catalog: %w", err)
	}
	return c, nil
}
