//go:build !minimal

// Offline help guides (GAP-UX-08) are a serve/full-build surface: the minimal
// binary keeps the stub in helpdocs_minimal.go so it stays under P13.

package main

import (
	"fmt"
	"io"
	"net/url"
	"strings"
)

// helpDoc is one compiled-in offline guide. The bytes come from the same
// tagged config struct and surface registry the binary uses, so an offline
// install always matches its own help.
type helpDoc struct {
	Name string
	Body string
}

// helpDocs renders the embedded documentation set (GAP-UX-08). The raw total
// is bounded by maxHelpDocsBytes, which also bounds the brotli size because
// compression never grows the payload; no network fetch exists on this path.
func helpDocs() []helpDoc {
	var out []helpDoc
	if ref, err := generateConfigReference(); err == nil {
		out = append(out, helpDoc{Name: "config", Body: ref})
	}
	var cmd strings.Builder
	cmd.WriteString("# Commands\n\n")
	for _, e := range callableSurfaces() {
		fmt.Fprintf(&cmd, "- `damnbot %s` — %s\n", e.Name, e.Summary)
	}
	cmd.WriteString("\nRun `damnbot help <command>` for flags; every command works offline.\n")
	out = append(out, helpDoc{Name: "commands", Body: cmd.String()}, helpDoc{Name: "guide", Body: "# Guide\n\n" +
		"Damn-Bot runs turns, tools, skills, memory and cron on the local machine.\n" +
		"Configuration lives in `config.toml`; secrets live in the OS keychain or `secrets.enc`.\n" +
		"Web UI: `damnbot serve open` prints a one-use link. Remote: `--remote` (10 min).\n" +
		"Session export: `damnbot sessions export <id> --format html` is self-contained.\n" +
		"Support bundle: `damnbot doctor bundle --out bundle.zip` (redacted, 0600).\n"})
	return out
}

// maxHelpDocsBytes bounds the embedded docs; 250 KiB is the SPEC §22.1
// GAP-UX-08 budget for the brotli payload, and the raw set stays below it.
const maxHelpDocsBytes = 250 * 1024

// helpDocsCommand implements `damnbot help docs [query]` and `damnbot help
// docs` offline. A query filters the guide bodies by case-insensitive
// substring; no index file or network is read.
func helpDocsCommand(args []string, stdout, stderr io.Writer) int {
	docs := helpDocs()
	if len(args) > 1 {
		say(stderr, "help docs: at most one query expected\n")
		return 2
	}
	if len(args) == 0 {
		for _, d := range docs {
			say(stdout, "%-10s %d bytes\n", d.Name, len(d.Body))
		}
		return 0
	}
	query := strings.Join(args, " ")
	q := strings.ToLower(query)
	found := false
	for _, d := range docs {
		if !strings.Contains(strings.ToLower(d.Body), q) && !strings.Contains(strings.ToLower(d.Name), q) {
			continue
		}
		found = true
		say(stdout, "## %s\n\n", d.Name)
		if _, err := io.WriteString(stdout, d.Body); err != nil {
			return 1
		}
	}
	if !found {
		say(stdout, "no offline documentation matches %q\n", cleanArg(query))
		return 0
	}
	return 0
}

// externalURL reports whether text references a non-relative URL. Every
// legitimate asset, export and doc is relative or inline.
func externalURL(text string) bool {
	for _, scheme := range []string{"http://", "https://", "//"} {
		for i := 0; ; i++ {
			j := strings.Index(text[i:], scheme)
			if j < 0 {
				break
			}
			i += j
			u, err := url.Parse(text[i:min(len(text), i+256)])
			if err == nil && u.Host != "" {
				return true
			}
		}
	}
	return false
}
