package main

import (
	"flag"
	"fmt"
	"io"
)

// Fase pemilik setiap bentuk permukaan (ADR-064, ADR-080). PHASE repo = F3.
const (
	phaseF1 = 1
	phaseF2 = 2
	phaseF3 = 3
	phaseF4 = 4
	phaseF5 = 5
	phaseF7 = 7
	phaseF8 = 8
)

// Status permukaan yang dipakai help dan (nanti) gate dokumen (ADR-197).
const (
	surfaceAvailable = "available"
	surfaceBuiltin   = "builtin"
)

// surfaceEntry adalah satu baris perintah tabel SPEC §10. Run yang nil berarti
// bentuk itu terdokumentasi tetapi belum ada: ia tidak muncul di help dan
// tidak pernah di-dispatch (tidak boleh ada stub, ADR-197).
type surfaceEntry struct {
	Name    string
	Summary string // ASCII, <= 72 karakter, satu baris help
	Phase   int
	Run     func(args []string, stdout, stderr io.Writer) int
	Usage   func(io.Writer) // printer usage yang sudah ada; nil = baris generik
	Builtin bool            // `version`: perintah terdokumentasi yang juga builtin
}

// Status mengembalikan status fase entri: builtin, available, atau
// pending:F<n> (ADR-080: fase > PHASE wajib pending).
func (e surfaceEntry) Status() string {
	switch {
	case e.Builtin:
		return surfaceBuiltin
	case e.Run != nil:
		return surfaceAvailable
	default:
		return fmt.Sprintf("pending:F%d", e.Phase)
	}
}

// surface adalah satu-satunya registry permukaan top-level (SPEC §10, ADR-197):
// urutannya mengikuti tabel §10, `bundles` menyusul setelah `skills`. `help`
// membaca tabel ini, dan gate dokumen kelak memakai fase yang sama.
func surface() []surfaceEntry {
	return []surfaceEntry{
		{Name: "run", Summary: "Run one headless turn", Phase: phaseF1, Run: runHeadless, Usage: runUsage},
		{Name: "acp", Summary: "ACP server on stdio", Phase: phaseF3},
		{Name: "serve", Summary: "Daemon: web UI, API, gateway, cron", Phase: phaseF4},
		{Name: "service", Summary: "systemd/launchd/Windows service unit", Phase: phaseF4},
		{Name: "setup", Summary: "Setup wizard, credentials, uninstall", Phase: phaseF3},
		{Name: "import", Summary: "Migrate from hermes, Claude Code, Codex, OpenClaw", Phase: phaseF3, Run: runImport, Usage: importUsage},
		{Name: "config", Summary: "Config, lazy modules, hooks, profiles", Phase: phaseF3, Run: runConfig, Usage: configUsage},
		{Name: "sessions", Summary: "Sessions, decision chain, prune, encrypted backup", Phase: phaseF2, Run: runSessions, Usage: sessionsUsage},
		{Name: "checkpoints", Summary: "Action journal: status, list, prune, clear", Phase: phaseF2, Run: runCheckpoints, Usage: checkpointUsage},
		{Name: "undo", Summary: "Universal undo and coding-checkpoint rollback", Phase: phaseF2, Run: runUndo, Usage: undoUsage},
		{Name: "review", Summary: "Memory and skill quarantine review", Phase: phaseF2, Run: runReview, Usage: reviewUsage},
		{Name: "skills", Summary: "Local skills: list, install, update, trust", Phase: phaseF2, Run: skillsCommand, Usage: skillsUsage},
		{Name: "bundles", Summary: "Skill bundles: list, show, create, delete, reload", Phase: phaseF2, Run: runBundles, Usage: bundlesUsage},
		{Name: "mcp", Summary: "MCP client and server", Phase: phaseF5},
		{Name: "gateway", Summary: "Messaging gateway", Phase: phaseF4},
		{Name: "cron", Summary: "Scheduler", Phase: phaseF4},
		{Name: "kanban", Summary: "Collaboration board", Phase: phaseF7},
		{Name: "usage", Summary: "Ledger, budget guard, insights", Phase: phaseF3},
		{Name: "eval", Summary: "Batch runs, evals, trajectory, scorecard", Phase: phaseF8},
		{Name: "doctor", Summary: "Checks, repair, diagnostics, maintenance", Phase: phaseF3},
		{Name: "update", Summary: "Atomic self-update with rollback and channels", Phase: phaseF3},
		{Name: "version", Summary: "Version, commit, DB schema, compiled-in modules", Phase: phaseF1, Run: runVersion, Builtin: true},
	}
}

// lookupSurface mencari nama perintah persis (tanpa alias: SPEC §10 menolak
// alias diam-diam).
func lookupSurface(name string) (surfaceEntry, bool) {
	for _, e := range surface() {
		if e.Name == name {
			return e, true
		}
	}
	return surfaceEntry{}, false
}

// surfaceHelp implements `damnbot help [topik]`, `damnbot --help`, and
// `damnbot <cmd> --help`. Topik yang terdokumentasi tetapi belum ada
// dilaporkan apa adanya (exit 0); topik tak dikenal exit 2.
func surfaceHelp(stdout, stderr io.Writer, topic []string) int {
	switch len(topic) {
	case 0:
		writeSurfaceList(stdout)
		return 0
	case 1:
	default:
		sessionsPrintf(stderr, "help: one topic expected, got %d\n", len(topic))
		return 2
	}
	e, ok := lookupSurface(topic[0])
	if !ok {
		sessionsPrintf(stderr, "help: unknown topic %q\n", cleanArg(topic[0]))
		sessionsPrintf(stderr, "help: run `damnbot help` for the command list\n")
		return 2
	}
	sessionsPrintf(stdout, "damnbot %s - %s\n", e.Name, e.Summary)
	sessionsPrintf(stdout, "phase: F%d (%s)\n\n", e.Phase, e.Status())
	switch {
	case e.Usage != nil:
		e.Usage(stdout)
	case e.Run != nil:
		sessionsPrintf(stdout, "usage: damnbot %s [flags]\n", e.Name)
	default:
		sessionsPrintf(stdout, "not callable yet: this command lands in phase F%d\n", e.Phase)
	}
	return 0
}

// writeSurfaceList mencetak perintah yang bisa dipanggil sekarang. Bentuk
// berfase > PHASE tidak muncul sampai fasenya tiba (ADR-080).
func writeSurfaceList(w io.Writer) {
	entries := callableSurfaces()
	width := 0
	for i := range entries {
		width = max(width, len(entries[i].Name))
	}
	sessionsPrintf(w, "usage: damnbot <command> [flags]\n\nCommands:\n")
	for i := range entries {
		sessionsPrintf(w, "  %-*s  %s\n", width, entries[i].Name, entries[i].Summary)
	}
	sessionsPrintf(w, "\nGlobal flags:\n")
	sessionsPrintf(w, "  --profile <name>  use a separate config and data profile\n")
	sessionsPrintf(w, "  -h, --help        print help for a command\n")
	sessionsPrintf(w, "  --version         print the version (alias of `damnbot version`)\n")
	sessionsPrintf(w, "\nRun `damnbot help <command>` for details.\n")
}

// callableSurfaces mengembalikan entri yang punya Run, urut §10.
func callableSurfaces() []surfaceEntry {
	var out []surfaceEntry
	for _, e := range surface() {
		if e.Run != nil {
			out = append(out, e)
		}
	}
	return out
}

// runVersion implements `damnbot version [--verbose]` (SPEC §10, builtin).
// `--version`/`-version` at the root stay aliases of this command.
func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verbose := fs.Bool("verbose", false, "probe the SQLite engine (REL-04)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		sessionsPrintf(stderr, "version: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	if _, err := fmt.Fprintln(stdout, "damnbot", version); err != nil {
		return 1
	}
	if *verbose {
		return printEngine(stdout)
	}
	return 0
}
