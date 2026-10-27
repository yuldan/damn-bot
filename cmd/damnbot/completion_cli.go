package main

import (
	"io"
	"sort"
	"strings"
)

// completionSubcommands is the SPEC §10 subcommand vocabulary exposed to the
// dynamic completer. It is data only: every entry is documented in SPEC §10.
func completionSubcommands() map[string][]string {
	return map[string][]string{
		"sessions":        {"ls", "show", "search", "fork", "rewind", "export", "why", "prune", "backup", "restore"},
		"checkpoints":     {"status", "list", "prune", "clear", "clear-legacy"},
		"undo":            {"ls"},
		"skills":          {"sync", "search", "install", "update", "list"},
		"bundles":         {"list", "show", "create", "delete", "reload"},
		"config":          {"show", "edit", "explain", "schema", "modules", "hooks", "profiles"},
		"usage":           {"budget", "insights"},
		"doctor":          {"health", "gc", "pprof", "bundle", "licenses", "tools", "index", "grammars", "voice", "gateway"},
		"import":          {"hermes", "claude-code", "codex", "openclaw", "archive"},
		"setup":           {"auth", "secrets", "ssh", "completion"},
		"config profiles": {"ls", "export", "import"},
		"setup auth":      {"login", "logout"},
		"setup secrets":   {"set", "rotate", "ls"},
	}
}

func completionFlags() []string { return []string{"--help", "--profile", "--version"} }

// runComplete implements the hidden `damnbot __complete <words...>`: it never
// touches config, data, network, or stdout beyond the candidate list.
func runComplete(args []string, stdout io.Writer) int {
	prefix := ""
	if len(args) > 0 {
		prefix = args[len(args)-1]
	}
	path := args
	if len(path) > 0 {
		path = path[:len(path)-1]
	}
	var candidates []string
	switch {
	case strings.HasPrefix(prefix, "-"):
		candidates = completionFlags()
	case len(path) == 0:
		candidates = completionTop()
	default:
		candidates = completionSubcommands()[strings.Join(path, " ")]
	}
	sort.Strings(candidates)
	for _, c := range uniqueSorted(candidates) {
		if !strings.HasPrefix(c, prefix) {
			continue
		}
		if code := say(stdout, "%s\n", c); code != 0 {
			return code
		}
	}
	return 0
}

func completionTop() []string {
	out := make([]string, 0, 24)
	for _, e := range callableSurfaces() {
		out = append(out, e.Name)
	}
	return out
}

func uniqueSorted(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

func setupCompletion(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, "setup completion: one shell expected (bash, zsh, fish, powershell)\n")
		return 2
	}
	script, ok := completionScript(args[0])
	if !ok {
		say(stderr, "setup completion: unsupported shell %q\n", cleanArg(args[0]))
		return 2
	}
	if code := say(stdout, "%s", script); code != 0 {
		return code
	}
	return 0
}

func completionScript(shell string) (string, bool) {
	switch shell {
	case "bash":
		return `# damnbot completion (bash)
_damnbot_complete() {
  local IFS=$'\n'
  COMPREPLY=($(damnbot __complete "${COMP_WORDS[@]:1:$COMP_CWORD}"))
}
complete -F _damnbot_complete damnbot
`, true
	case "zsh":
		return `#compdef damnbot
_damnbot() {
  local -a candidates
  candidates=("${(@f)$(damnbot __complete "${words[@]:1}")}")
  _describe 'damnbot' candidates
}
compdef _damnbot damnbot
`, true
	case "fish":
		return `# damnbot completion (fish)
complete -c damnbot -f -a '(damnbot __complete (commandline -opc)[2..-1])'
`, true
	case "powershell":
		return `# damnbot completion (PowerShell)
Register-ArgumentCompleter -Native -CommandName damnbot -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $words = $commandAst.ToString().Split(' ')
  damnbot __complete @($words[1..($words.Length - 1)]) | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
  }
}
`, true
	default:
		return "", false
	}
}
