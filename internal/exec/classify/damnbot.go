package classify

import (
	"path"
	"strings"
)

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

// damnbotValuedFlags are the global options that consume the next word.
const damnbotValuedFlags = " --profile --config --data-dir -p "

// damnbotBoolFlags are the global options that take no value. Any other
// option before the subcommand is Unknown (SEC15-12): an unrecognized option
// may take a value, which would shift the group word out of the parse and
// leave the real maintenance command looking like a plain mutating one.
const damnbotBoolFlags = " --help -h --version -v --verbose --force-migrate --json -- "

// damnbotVerbs are the destructive maintenance verbs. Together with --yes
// they are a second, group-independent signal (SEC15-11), so hiding or
// mistyping the group word cannot smuggle a real purge/clear/prune through.
const damnbotVerbs = " purge prune clear clear-legacy archive restore gc "

// damnbotWords returns the positional command words after global options. ok
// is false when an unrecognized option appears before the subcommand; grouped
// is true when the first positional word is not a literal plain word, so the
// shell could expand it into a maintenance group at execution time.
func damnbotWords(args []arg) (words []string, grouped, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		v := strings.ToLower(a.s)
		switch {
		case inList(damnbotValuedFlags, v) && i+1 < len(args):
			i++
		case strings.HasPrefix(v, "--profile="), strings.HasPrefix(v, "--config="),
			strings.HasPrefix(v, "--data-dir="), strings.HasPrefix(v, "-p="), v == "--force-migrate":
		case strings.HasPrefix(v, "-"):
			if len(words) == 0 && !inList(damnbotBoolFlags, v) {
				return nil, false, false // SEC15-12
			}
		default:
			if len(words) == 0 && (a.glob || !a.lit || strings.ContainsAny(a.s, "*?[]")) {
				grouped = true // SEC15-11: sess*, [s]essions, ?essions, $G, ...
			}
			words = append(words, v)
		}
	}
	return words, grouped, true
}

// hasVerb reports whether any positional word is a destructive verb.
func hasVerb(words []string) bool {
	for _, w := range words {
		if inList(damnbotVerbs, w) {
			return true
		}
	}
	return false
}

// damnbotGroups are the subcommand groups that own destructive maintenance
// verbs (SEC15-2).
const damnbotGroups = " sessions checkpoints backup restore doctor setup update "

// hasYes reports an explicit confirmation flag.
func hasYes(args []arg) bool { return hasFlag(args, "--yes") || hasFlag(args, "-y") }

// damnbot classifies maintenance subcommands of our own CLI. They must never
// auto-run: the damnbot process writes the DB itself, so deny_paths does not
// protect the data directory (SEC15-2).
func (s *state) damnbot(args []arg) {
	words, grouped, ok := damnbotWords(args)
	if !ok {
		s.raise(Unknown, "damnbot: unrecognized global option before the subcommand")
		return
	}
	if grouped {
		s.raise(Unknown, "damnbot: unexpanded group token "+at(words, 0))
		return
	}
	group := at(words, 0)
	switch {
	case hasYes(args) && hasVerb(words):
		s.raise(Destructive, "damnbot maintenance verb --yes: "+group)
	case hasYes(args) && inList(damnbotGroups, group):
		s.raise(Destructive, "damnbot maintenance --yes: "+group)
	case damnbotMaintenance(words, args, group):
		s.raise(Destructive, "damnbot destructive maintenance: "+group+" "+at(words, 1))
	}
}

// isDamnbotName recognizes the binary by base name, including Windows
// extensions and paths (./damnbot, /usr/bin/damnbot, C:\tools\damnbot.exe).
func isDamnbotName(name string) bool {
	n := strings.TrimRight(strings.ToLower(name), ".")
	n = strings.TrimSuffix(n, ".exe")
	n = strings.TrimSuffix(n, ".cmd")
	n = strings.TrimSuffix(n, ".bat")
	return n == "damnbot" || strings.HasPrefix(n, "damnbo~") // Windows 8.3 (DAMNBO~1)
}

// damnbotCommand recognizes a path/symlink whose resolved base is the
// damnbot binary. A workspace copy with another name has no static identity
// (documented residual in ADR-167).
func (s *state) damnbotCommand(a0, name string) bool {
	if isDamnbotName(name) {
		return true
	}
	p, ok := s.abs(a0)
	if !ok {
		return false
	}
	return isDamnbotName(path.Base(s.resolve(p)))
}
