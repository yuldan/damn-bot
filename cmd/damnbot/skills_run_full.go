//go:build !minimal

package main

import "io"

// runSkills implements the local half of `damnbot skills` (CLI-046) plus the
// F8 network hub: list/inspect/check/update/index/diff/audit/ledger/config/
// install/uninstall/hub.
func runSkills(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		skillsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return skillsList(args[1:], stdout, stderr)
	case "inspect":
		return skillsInspect(args[1:], stdout, stderr)
	case "check":
		return skillsCheck(args[1:], stdout, stderr)
	case "update", "index":
		return skillsUpdate(args[1:], stdout, stderr, args[0] == "index")
	case "diff":
		return skillsDiff(args[1:], stdout, stderr)
	case "audit", "ledger":
		return skillsAudit(args[1:], stdout, stderr)
	case "config":
		return skillsConfig(args[1:], stdout, stderr)
	case "reset":
		return skillsReset(args[1:], stdout, stderr)
	case "install":
		return skillsInstall(args[1:], stdout, stderr)
	case "uninstall":
		return skillsUninstall(args[1:], stdout, stderr)
	case "hub":
		return skillsHubCommand(args[1:], stdout, stderr)
	default:
		say(stderr, "skills: unknown subcommand %q\n", skillsArg(args[0]))
		skillsUsage(stderr)
		return 2
	}
}
