package classify

import "strings"

// damnbotMaintenance is true for commands whose destructive maintenance
// verbs delete journals/session data or replace the database (SEC15-2).
// The list mirrors the CLI help in SPEC §10: sessions purge/prune/archive/
// restore (including sessions backup restore), checkpoints clear/prune,
// backup restore, doctor gc, setup --uninstall, update --rollback --data,
// and any maintenance group carrying --yes.
func damnbotMaintenance(words []string, args []arg, group string) bool {
	switch group {
	case "sessions":
		switch at(words, 1) {
		case "purge", "prune", "archive", "restore":
			return true
		case "backup":
			return at(words, 2) == "restore"
		}
	case "checkpoints":
		switch at(words, 1) {
		case "clear", "clear-legacy", "prune":
			return true
		}
	case "backup", "restore":
		return at(words, 1) == "restore" || group == "restore"
	case "doctor":
		return at(words, 1) == "gc"
	case "setup":
		return hasFlag(args, "--uninstall") || hasFlag(args, "--purge-data")
	case "update":
		return hasFlag(args, "--rollback") && hasFlag(args, "--data")
	}
	return false
}

// damnbotWords returns the positional command words after global options.
func damnbotWords(args []arg) []string {
	var words []string
	for i := 0; i < len(args); i++ {
		v := strings.ToLower(args[i].s)
		switch {
		case v == "--profile" && i+1 < len(args):
			i++
		case strings.HasPrefix(v, "--profile="), v == "--force-migrate":
		case strings.HasPrefix(v, "-"):
		default:
			words = append(words, v)
		}
	}
	return words
}

// damnbot classifies maintenance subcommands of our own CLI. They must never
// auto-run: the damnbot process writes the DB itself, so deny_paths does not
// protect the data directory (SEC15-2).
func (s *state) damnbot(args []arg) {
	words := damnbotWords(args)
	group := at(words, 0)
	maintenance := group == "sessions" || group == "checkpoints" || group == "backup" ||
		group == "restore" || group == "doctor" || group == "setup" || group == "update"
	if maintenance && (hasFlag(args, "--yes") || hasFlag(args, "-y")) {
		s.raise(Destructive, "damnbot maintenance --yes: "+group)
		return
	}
	if damnbotMaintenance(words, args, group) {
		s.raise(Destructive, "damnbot destructive maintenance: "+group+" "+at(words, 1))
	}
}

// isDamnbotName recognizes the binary by base name, including Windows
// extensions and paths (./damnbot, /usr/bin/damnbot, C:\tools\damnbot.exe).
func isDamnbotName(name string) bool {
	n := strings.TrimSuffix(strings.ToLower(name), ".exe")
	n = strings.TrimSuffix(n, ".cmd")
	n = strings.TrimSuffix(n, ".bat")
	return n == "damnbot"
}
