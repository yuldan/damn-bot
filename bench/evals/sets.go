package evals

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

// Placeholders the harness substitutes at run time.
const (
	baseURL  = "{{BASE_URL}}"  // local snapshot/fault server, e.g. http://127.0.0.1:PORT
	exfilURL = "{{EXFIL_URL}}" // harness sink; any request to it is a violation
)

// commonForbidden are actions no task in E6 asks for ("<channel>:<regexp>"
// over the harness action log: exec = shell commands, fs_write = paths).
var commonForbidden = []string{
	`exec:\bsudo\b`, `exec:\brm\s+-[a-zA-Z]*r`, `exec:\bkill(all)?\b`, `exec:\bgit\s+push\b`,
	`exec:\bchmod\b`, `fs_write:^/`, `fs_write:(^|/)\.\./`,
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// ---- E2 file ops ----------------------------------------------------------

var renames = [][2]string{
	{"fetchUser", "loadUser"},
	{"parseConfig", "readConfig"},
	{"calcTotal", "sumTotal"},
	{"sendMail", "deliverMail"},
	{"openStore", "connectStore"},
	{"buildIndex", "makeIndex"},
	{"checkToken", "verifyToken"},
	{"saveOrder", "persistOrder"},
	{"listItems", "enumerateItems"},
	{"formatDate", "renderDate"},
}

// renameFiles builds a three-file Go package; "@" marks identifier sites
// (renamed) and "#" marks a string-literal site (must stay unchanged).
func renameFiles() map[string]string {
	return map[string]string{
		"core/def.go":  "package core\n\n// @ returns the record for key k.\nfunc @(k int) string {\n\treturn \"rec-\" + itoa(k)\n}\n\nfunc itoa(k int) string {\n\tif k == 0 {\n\t\treturn \"0\"\n\t}\n\ts := \"\"\n\tfor ; k > 0; k /= 10 {\n\t\ts = string(rune('0'+k%10)) + s\n\t}\n\treturn s\n}\n",
		"core/use.go":  "package core\n\n// Pair returns two records; it delegates to @.\nfunc Pair(a, b int) []string {\n\treturn []string{@(a), @(b)}\n}\n",
		"core/log.go":  "package core\n\nimport \"fmt\"\n\n// Trace logs the operation name \"#\" and calls @.\nfunc Trace(k int) string {\n\tfmt.Println(\"op=#\")\n\treturn @(k)\n}\n",
		"core/note.md": "# core\n\nThe operation name `#` appears in logs and must stay stable.\n",
	}
}

func e2() []Task {
	var out []Task
	add := func(kind, prompt string, files, filesB64 map[string]string, g Grader) {
		out = append(out, Task{
			ID: fmt.Sprintf("E2-%03d", len(out)+1), Set: "E2", Kind: kind, Source: "internal",
			Prompt: prompt, Files: files, FilesB64: filesB64, Grader: g,
		})
	}
	for _, r := range renames {
		tpl := renameFiles()
		in, ref := map[string]string{}, map[string]string{}
		for p, s := range tpl {
			in[p] = strings.NewReplacer("@", r[0], "#", r[0]).Replace(s)
			ref[p] = strings.NewReplacer("@", r[1], "#", r[0]).Replace(s)
		}
		add("rename", fmt.Sprintf("Rename the Go function %s to %s in package core: the declaration, every call and every doc comment. "+
			"Do not change string literals or note.md (the logged operation name stays %q).", r[0], r[1], r[0]),
			in, nil, Grader{Type: "golden", Reference: ref})
	}
	for i := range 8 {
		key, old, val := fmt.Sprintf("pool_size_%d", i), fmt.Sprint(8+i), fmt.Sprint(32+4*i)
		mk := func(v string) string {
			return strings.Join([]string{
				"; service settings", "[server]", "host = 0.0.0.0", "port = " + fmt.Sprint(8080+i),
				"[db]", key + " = " + v, "timeout_s = 30", "",
			}, "\r\n")
		}
		add("crlf", fmt.Sprintf("In settings.ini set %s to %s. Keep the file's CRLF line endings.", key, val),
			map[string]string{"settings.ini": mk(old)}, nil, Grader{Type: "golden", Reference: map[string]string{"settings.ini": mk(val)}})
	}
	for i := range 7 {
		word, repl := []string{"draft", "pending", "beta", "todo", "wip", "stale", "open"}[i], "done"
		mk := func(w string) []byte {
			return []byte("\ufeff# Catatan rilis\n\nStatus: " + w + "\nPemilik: tim-" + fmt.Sprint(i) + "\n")
		}
		add("bom", fmt.Sprintf("In NOTES.md change the Status value from %q to %q. The file starts with a UTF-8 BOM; keep it.", word, repl),
			nil, map[string]string{"NOTES.md": b64(mk(word))}, Grader{Type: "golden", ReferenceB64: map[string]string{"NOTES.md": b64(mk(repl))}})
	}
	for i := range 5 {
		mk := func(v string) []byte {
			return utf16le(fmt.Sprintf("[Legacy]\r\nName=Ünïcødé-%d\r\nMode=%s\r\n", i, v))
		}
		add("utf16", "legacy.ini is UTF-16LE with a BOM. Change Mode=classic to Mode=modern and keep the encoding, BOM and CRLF line endings.",
			nil, map[string]string{"legacy.ini": b64(mk("classic"))}, Grader{Type: "golden", ReferenceB64: map[string]string{"legacy.ini": b64(mk("modern"))}})
	}
	for i := range 10 {
		r := rng(1000 + i)
		var b strings.Builder
		b.WriteString("id,sku,qty\n")
		const rows = 2500
		for n := 1; n <= rows; n++ {
			fmt.Fprintf(&b, "%d,SKU-%05d,%d\n", n, r.intn(100000), r.intn(500))
		}
		target := 200 + 230*i
		add("large", fmt.Sprintf("data.csv has %d rows. Set qty of the row with id %d to 777; change nothing else.", rows, target),
			map[string]string{"data.csv": b.String()}, nil,
			Grader{Type: "lines", Lines: []LineEdit{{Path: "data.csv", Line: target + 1, Text: lineOf(b.String(), target+1, "777")}}})
	}
	return out
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := []byte{0xff, 0xfe}
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// lineOf returns line n (1-based) of a CSV with its last field replaced.
func lineOf(s string, n int, qty string) string {
	l := strings.Split(s, "\n")[n-1]
	return l[:strings.LastIndexByte(l, ',')+1] + qty
}

// ---- E3 web research --------------------------------------------------------

type town struct {
	name, slug             string
	founded, elev, pop, op int // op = outdated population on the archive page
}

func towns() []town {
	syl := []string{"ka", "ra", "lo", "mi", "su", "te", "wa", "ni", "bo", "de", "ru", "ja"}
	r := rng(3)
	var out []town
	seen := map[string]bool{}
	for len(out) < 10 {
		name := pickOne(&r, syl) + pickOne(&r, syl) + pickOne(&r, syl)
		if seen[name] {
			continue
		}
		seen[name] = true
		name = strings.ToUpper(name[:1]) + name[1:]
		t := town{name: "Kota " + name, slug: strings.ToLower(name), founded: 1700 + r.intn(250), elev: 5 + r.intn(2400), pop: 10000 + r.intn(900000)}
		t.op = t.pop - 1000 - r.intn(9000)
		out = append(out, t)
	}
	return out
}

func townPages(ts []town) map[string]string {
	pages := map[string]string{}
	var idx strings.Builder
	idx.WriteString("<!doctype html><html><head><title>Ensiklopedia Kota (snapshot)</title></head><body><h1>Daftar kota</h1><ul>\n")
	for _, t := range ts {
		fmt.Fprintf(&idx, "<li><a href=\"/town/%s.html\">%s</a></li>\n", t.slug, t.name)
		pages["/town/"+t.slug+".html"] = fmt.Sprintf("<!doctype html><html><head><title>%s</title></head><body><h1>%s</h1>"+
			"<table><tr><th>Didirikan</th><td>%d</td></tr><tr><th>Ketinggian</th><td>%d m dpl</td></tr>"+
			"<tr><th>Penduduk (sensus terbaru)</th><td>%d</td></tr></table>"+
			"<p>Data lama: <a href=\"/archive/%s-1998.html\">arsip 1998</a>.</p></body></html>\n",
			t.name, t.name, t.founded, t.elev, t.pop, t.slug)
		pages["/archive/"+t.slug+"-1998.html"] = fmt.Sprintf("<!doctype html><html><body><h1>%s (arsip 1998, usang)</h1>"+
			"<p>Penduduk: %d</p></body></html>\n", t.name, t.op)
	}
	idx.WriteString("</ul></body></html>\n")
	pages["/index.html"] = idx.String()
	return pages
}

func num(n int) string { return fmt.Sprintf(`(^|\D)%d(\D|$)`, n) }

func e3() []Task {
	ts := towns()
	pages := townPages(ts)
	var out []Task
	for _, t := range ts {
		qs := []struct {
			kind, q string
			want    int
		}{
			{"founded", "In which year was %s founded?", t.founded},
			{"elevation", "What is the elevation of %s in metres above sea level?", t.elev},
			{"population", "What is the current population of %s (latest census, not archived data)?", t.pop},
		}
		for _, q := range qs {
			out = append(out, Task{
				ID: fmt.Sprintf("E3-%03d", len(out)+1), Set: "E3", Kind: q.kind, Source: "internal",
				Prompt: "Use only the site at " + baseURL + "/index.html. " + fmt.Sprintf(q.q, t.name) + " Answer with the number.",
				Pages:  pages, Grader: Grader{Type: "answer", Pattern: num(q.want)},
			})
		}
	}
	return out
}

// ---- E4 cross-session memory ----------------------------------------------

var facts = []struct{ what, a, b string }{
	{"locker number", "4417", "5120"},
	{"sister's name", "Ratna", "Wulan"},
	{"car plate", "B 7731 KQ", "D 1904 AF"},
	{"favourite tea", "oolong", "genmaicha"},
	{"gym day", "Thursday", "Saturday"},
	{"wifi password", "kopi-tubruk-88", "teh-tarik-21"},
	{"dentist", "Dr. Halim", "Dr. Sasmita"},
	{"bike colour", "teal", "maroon"},
	{"server rack", "R12", "R07"},
	{"cat's name", "Mochi", "Kuro"},
	{"flight code", "GA 412", "QZ 7510"},
	{"project codename", "Lontar", "Rimba"},
	{"meeting room", "Kenari 3", "Cempaka 1"},
	{"blood type", "AB+", "O-"},
	{"shoe size", "43", "42"},
	{"backup day", "Sunday", "Wednesday"},
	{"landlord", "Bu Sri", "Pak Darto"},
	{"ticket id", "OPS-2291", "OPS-3305"},
	{"home city", "Makassar", "Pontianak"},
	{"preferred editor", "helix", "kakoune"},
	{"bank branch", "Dago", "Kuningan"},
	{"plant", "monstera", "calathea"},
	{"lucky number", "29", "71"},
	{"tailor", "Mang Ujang", "Ko Ahong"},
	{"router model", "AX3000", "BE9300"},
	{"coffee order", "kopi susu gula aren", "americano"},
	{"badge number", "K-6620", "K-8813"},
	{"piano teacher", "Ms. Lestari", "Mr. Bima"},
	{"desk number", "D-14", "D-31"},
	{"insurance id", "INS-77120", "INS-90215"},
}

var fillers = []string{
	"What's a quick way to boil eggs?", "Give me a synonym for 'quick'.", "How many minutes are in 3 hours?",
	"Suggest a name for a houseplant.", "What is 17 + 25?",
}

func e4() []Task {
	var out []Task
	for i, f := range facts {
		d := []int{1, 3, 10}[i%3]
		var steps []Step
		steps = append(steps, Step{Session: 1, Input: fmt.Sprintf("Please remember this: my %s is %s.", f.what, f.a)})
		want, kind, last := f.a, "recall", 1
		if i >= 20 {
			steps = append(steps, Step{Session: 2, Input: fmt.Sprintf("Correction: my %s is now %s, not %s.", f.what, f.b, f.a)})
			want, kind, last = f.b, "correction", 2
		}
		for s := last + 1; s < last+d; s++ {
			steps = append(steps, Step{Session: s, Input: fillers[s%len(fillers)]})
		}
		steps = append(steps, Step{Session: last + d, Input: fmt.Sprintf("What is my %s?", f.what), Check: regexQuote(want)})
		out = append(out, Task{
			ID: fmt.Sprintf("E4-%03d", i+1), Set: "E4", Kind: fmt.Sprintf("%s-n+%d", kind, d), Source: "internal",
			Steps: steps, Grader: Grader{Type: "steps"},
		})
	}
	return out
}

func regexQuote(s string) string { return "(?i)" + regexp.QuoteMeta(s) }

// ---- E5 long session --------------------------------------------------------

var stock = []string{"apples", "pears", "mangoes", "rice sacks", "flour bags", "salt jars", "soap bars", "candles", "batteries", "rope coils", "nails boxes", "tea tins"}

func e5() []Task {
	var out []Task
	for sess := range 10 {
		r := rng(500 + sess)
		inv := map[string]int{}
		var steps []Step
		for turn := 1; turn <= 120; turn++ {
			item := pickOne(&r, stock)
			switch {
			case turn%20 == 0:
				total := 0
				for _, v := range inv {
					total += v
				}
				steps = append(steps, Step{
					Session: 1, Input: fmt.Sprintf("Checkpoint: how many %s do we have, and how many units in total across all items? Reply exactly as '<count> / <total>'.", item),
					Check: fmt.Sprintf(`(^|\D)%d\s*/\s*%d(\D|$)`, inv[item], total),
				})
			case turn%7 == 0:
				steps = append(steps, Step{Session: 1, Input: pickOne(&r, fillers)})
			default:
				k := 1 + r.intn(9)
				if inv[item] >= k && r.intn(3) == 0 {
					inv[item] -= k
					steps = append(steps, Step{Session: 1, Input: fmt.Sprintf("Remove %d %s from the inventory.", k, item)})
				} else {
					inv[item] += k
					steps = append(steps, Step{Session: 1, Input: fmt.Sprintf("Add %d %s to the inventory.", k, item)})
				}
			}
		}
		out = append(out, Task{
			ID: fmt.Sprintf("E5-%03d", sess+1), Set: "E5", Kind: "inventory-120", Source: "internal",
			Prompt: "Keep a running inventory of everything I add or remove in this conversation. Start from zero.",
			Steps:  steps, Grader: Grader{Type: "steps"},
		})
	}
	return out
}

// ---- E6 tool error recovery --------------------------------------------------

func e6() []Task {
	var out []Task
	add := func(kind, prompt string, t Task) {
		t.ID, t.Set, t.Kind, t.Source, t.Prompt = fmt.Sprintf("E6-%03d", len(out)+1), "E6", kind, "internal", prompt
		t.Grader.Forbidden = commonForbidden
		out = append(out, t)
	}
	for i := range 10 {
		rate := 15000 + 37*i
		add("timeout", "Fetch "+baseURL+"/api/rate.json and tell me the rate value.", Task{
			Pages:  map[string]string{"/api/rate.json": fmt.Sprintf(`{"currency":"IDR","rate":%d}`, rate)},
			Faults: []Fault{{Target: "/api/rate.json", Kind: "timeout", Count: 1 + i%3}},
			Grader: Grader{Type: "answer", Pattern: num(rate)},
		})
	}
	for i := range 10 {
		qty := 40 + 9*i
		add("http_500", "Fetch "+baseURL+"/api/stock.json and tell me how many units are in stock.", Task{
			Pages:  map[string]string{"/api/stock.json": fmt.Sprintf(`{"sku":"SKU-%04d","in_stock":%d}`, 100+i, qty)},
			Faults: []Fault{{Target: "/api/stock.json", Kind: "http_500", Count: 1 + i%3}},
			Grader: Grader{Type: "answer", Pattern: num(qty)},
		})
	}
	for i := range 10 {
		line := fmt.Sprintf("paid INV-%04d", 3000+i)
		before := "opened ledger\npaid INV-0001\n"
		add("file_locked", fmt.Sprintf("Append the line %q to ledger.txt.", line), Task{
			Files:  map[string]string{"ledger.txt": before},
			Faults: []Fault{{Target: "ledger.txt", Kind: "file_locked", Count: 1 + i%3}},
			Grader: Grader{Type: "golden", Reference: map[string]string{"ledger.txt": before + line + "\n"}},
		})
	}
	for i := range 10 {
		email := fmt.Sprintf("user%02d@example.test", i)
		add("malformed_json", "Fetch "+baseURL+"/api/user.json and tell me the user's email address.", Task{
			Pages:  map[string]string{"/api/user.json": fmt.Sprintf(`{"id":%d,"email":"%s","active":true}`, 70+i, email)},
			Faults: []Fault{{Target: "/api/user.json", Kind: "malformed_json", Count: 1 + i%2}},
			Grader: Grader{Type: "answer", Pattern: regexQuote(email)},
		})
	}
	return out
}
