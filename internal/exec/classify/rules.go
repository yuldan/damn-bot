package classify

import (
	"slices"
	"strings"
)

// Label tables (EXEC-AND-CODING §A.8, SPEC §22.1 GAP-SEC-12/15), space
// delimited so lookups need no package-level map (gochecknoglobals). Name
// keys are "name", "name:sub" or "name:sub:sub2" over positional words;
// flag keys are "name:sub:flag" or "name::flag" (short clusters split).
const (
	destructiveKeys = " fdisk sfdisk parted wipefs shred shutdown reboot halt poweroff format diskpart" +
		" systemctl:reboot systemctl:poweroff systemctl:halt init:0 init:6 reg:delete git:filter-branch" +
		" git:filter-repo git:stash:drop git:stash:clear kubectl:delete terraform:destroy docker:volume:rm" +
		" docker:volume:prune podman:volume:rm "
	destructiveFlags = " git:reset:--hard git:clean:-f git:clean:--force git:checkout:. git:checkout:-f" +
		" git:checkout:--force git:restore:. git:restore::/ git:branch:-D git:config:--global git:config:--system" +
		" git:push:-f git:push:--force git:push:--force-with-lease git:push:--force-if-includes git:push:--delete" +
		" git:push:-d git:push:--mirror git:push:--prune git:push:+ git:push:: crontab::-r kill::-1" +
		" docker:system:-a docker:system:--all podman:system:-a "
	irreversibleKeys = " git:push git:send-email npm:publish npm:unpublish npm:deprecate npm:dist-tag" +
		" pnpm:publish yarn:publish bun:publish cargo:publish cargo:yank twine:upload gem:push gem:yank" +
		" poetry:publish uv:publish docker:push podman:push helm:push terraform:apply yarn:global" +
		" gh:release gh:pr:create gh:pr:merge gh:pr:close gh:pr:comment gh:issue:create gh:issue:comment" +
		" gh:issue:close gh:repo:create gh:repo:delete gh:repo:edit gh:secret:set gh:workflow:run" +
		" glab:release glab:mr:create glab:mr:merge glab:mr:close glab:mr:note glab:issue:create" +
		" glab:issue:note glab:issue:close glab:repo:create glab:repo:delete glab:variable:set glab:ci:run "
	irreversibleFlags = " npm::-g npm::--global pnpm::-g pnpm::--global gh:api:-X gh:api:--method gh:api:-f" +
		" gh:api:-F gh:api:--field gh:api:--raw-field gh:api:--input "
	monetaryKeys = " link-cli:spend-request stripe:charges:create stripe:payment_intents:create" +
		" stripe:transfers:create stripe:payouts:create solana:transfer cast:send gh:sponsor "
	networkKeys = " npm:install npm:i npm:ci npm:add npm:update pnpm:install pnpm:i pnpm:add pnpm:update" +
		" yarn yarn:install yarn:add bun:install bun:add go:get go:install go:mod:download cargo:fetch" +
		" cargo:install cargo:update git:clone git:fetch git:pull git:ls-remote git:submodule docker:pull" +
		" uv:pip:install uv:add uv:sync gh glab kubectl helm "
	nonMutatingKeys = " ls cat head tail wc pwd echo stat file tree du df which whoami id date uname grep" +
		" egrep fgrep rg cut nl od xxd md5sum sha1sum sha256sum sha512sum true false find git:status" +
		" git:diff git:log git:show git:rev-parse git:ls-files git:blame git:describe go:list go:version go:doc "
	sysPackagers = " apt apt-get dnf yum pacman zypper apk brew port snap flatpak choco winget scoop "
	pkgReadSubs  = " search list show info policy outdated help -Q -Ss -Si "
	agents       = " claude codex gemini grok opencode aider goose openhands blackbox antigravity cursor-agent" +
		" qwen amp crush omp copilot "
	agentBypass = " --dangerously-skip-permissions --dangerously-bypass-approvals-and-sandbox --yolo" +
		" --always-approve --auto-approve --full-auto --allow-all-tools --trust-all-tools" +
		" --approval-mode=yolo --permission-mode=bypassPermissions gemini:-y qwen:-y "
)

func hasKey(table string, keys [3]string) bool {
	return inList(table, keys[0]) || inList(table, keys[1]) || inList(table, keys[2])
}

// mentions reports whether some entry of list starts with name.
func mentions(list, name string) bool { return strings.Contains(list, " "+name) }

// tables applies the name/subcommand label tables; names in no table (most
// commands) skip the lookups.
func (s *state) tables(name string, keys [3]string, first string) {
	if !mentions(destructiveKeys+irreversibleKeys+monetaryKeys+networkKeys+sysPackagers+" source . ", name) {
		if strings.HasPrefix(name, "mkfs") {
			s.raise(Destructive, "destructive: "+name)
		}
		return
	}
	if hasKey(destructiveKeys, keys) {
		s.raise(Destructive, "destructive: "+keys[1])
	}
	if hasKey(irreversibleKeys, keys) || (inList(sysPackagers, name) && !inList(pkgReadSubs, first)) {
		s.raise(Irreversible, "irreversible: "+keys[1])
	}
	if hasKey(monetaryKeys, keys) {
		s.raise(Monetary, "monetary: "+keys[1])
	}
	if hasKey(networkKeys, keys) || inList(sysPackagers, name) {
		s.flag(Network, "network: "+name)
	}
	if name == "source" || name == "." {
		s.raise(Unknown, "source runs a file")
	}
}

// flagRules applies the option tables: each entry "name:sub:flag" (sub
// empty = any) whose flag some argument carries.
func (s *state) flagRules(name, sub string, args []arg) {
	if !mentions(destructiveFlags+irreversibleFlags, name+":") {
		return
	}
	for _, t := range [...]struct {
		table string
		class Class
	}{{destructiveFlags, Destructive}, {irreversibleFlags, Irreversible}} {
		for e := range strings.FieldsSeq(t.table) {
			rest, ok := strings.CutPrefix(e, name)
			esub, f, _ := strings.Cut(strings.TrimPrefix(rest, ":"), ":")
			if ok && strings.HasPrefix(rest, ":") && (esub == "" || esub == sub) && hasFlag(args, f) {
				s.raise(t.class, t.class.String()+": "+name+" "+sub+" "+f)
			}
		}
	}
}

// hasFlag reports an argument equal to f, --f=value, a short cluster
// containing f (-fdx has -f), or a "+"/":" refspec prefix.
func hasFlag(args []arg, f string) bool {
	return slices.ContainsFunc(args, func(a arg) bool {
		k, _, _ := strings.Cut(a.s, "=")
		cluster := len(f) == 2 && f[0] == '-' && isLetter(f[1]) && len(a.s) > 2 && a.s[0] == '-' && isLetter(a.s[1]) &&
			strings.IndexByte(a.s, f[1]) > 0
		return a.s == f || k == f || cluster || ((f == "+" || f == ":") && strings.HasPrefix(a.s, f))
	})
}

// changeDir labels a literal `cd` into a payment skill directory Monetary
// (SEC10-1) and keeps cwd untracked afterwards, so every relative operand
// stays unknown (ADR-079 butir 2).
func (s *state) changeDir(args []arg) {
	tracked, targets := true, 0
	for _, a := range args {
		if strings.HasPrefix(a.s, `\\`) || (s.fold && strings.HasPrefix(a.s, "//")) {
			s.raise(Unknown, "remote cd target: "+a.s) // SEC10-22: never open a share
			tracked = false
			continue
		}
		if !a.lit || a.glob || strings.HasPrefix(a.s, "-") || (s.fold && strings.HasPrefix(a.s, "/")) {
			continue
		}
		targets++
		p, ok := s.abs(a.s)
		if !ok {
			tracked = false
			continue
		}
		s.paymentFile(p, s.resolve(p))
	}
	s.cdTracked = (targets == 0 && s.cdTracked) || (targets > 0 && tracked) // bare cd: no-op (SEC10-25b)
	s.cwdOK = false
}

// tool handles commands whose operands need specific parsing.
func (s *state) tool(name, sub string, args []arg) {
	switch name {
	case "git":
		s.gitPaths(sub, args[1:])
	case "find":
		s.find(args[1:])
	case "cd", "pushd", "chdir", "set-location", "sl", "push-location":
		s.changeDir(args[1:])
	case "popd":
		s.cwdOK = false
	case "pip", "pip3":
		s.pip(sub, args)
	case "dd":
		for _, a := range args[1:] {
			if k, v, ok := strings.Cut(a.s, "="); ok && (k == "if" || k == "of") {
				s.operand(arg{s: v, lit: a.lit, glob: a.glob}, k == "of")
			}
		}
	default:
		if inList(agents, name) {
			s.agent(name, args[1:])
		}
		s.operands(name, args[1:])
	}
}

// agent labels delegated agent CLIs run with an approval-bypass flag
// (GAP-SEC-12).
func (s *state) agent(name string, args []arg) {
	for i, a := range args {
		v := a.s
		if i+1 < len(args) && (v == "--approval-mode" || v == "--permission-mode") {
			v += "=" + args[i+1].s
		}
		if inList(agentBypass, v) || inList(agentBypass, name+":"+v) {
			s.raise(DelegatedAgentBypass, name+" "+v)
			return
		}
	}
	s.note("external agent: consider a worktree or container backend")
}

// pip: installs inside a venv under the root are mutating + network,
// otherwise irreversible (system site-packages).
func (s *state) pip(sub string, args []arg) {
	if sub != "install" && sub != "uninstall" && sub != "download" {
		return
	}
	s.flag(Network, "network: pip")
	venv := s.env.Venv
	if p, ok := s.abs(venv); sub == "download" || (venv != "" && ok && s.inside(p) && !hasArg(args, "--user")) {
		return
	}
	s.raise(Irreversible, "pip "+sub+" outside a workspace venv")
}

// gitGlobals strips git's global options: -C dir is a read operand, -c
// and --exec-path can run arbitrary programs (Unknown).
func (s *state) gitGlobals(args []arg) []arg {
	i := 1
	for ; i < len(args) && strings.HasPrefix(args[i].s, "-"); i++ {
		switch a := args[i].s; {
		case a == "-c" || strings.HasPrefix(a, "--exec-path") || strings.HasPrefix(a, "--config-env"):
			s.raise(Unknown, "git config override "+a)
			i++
		case a == "-C" && i+1 < len(args):
			s.operand(args[i+1], false)
			i++
		}
	}
	return append([]arg{args[0]}, args[min(i, len(args)):]...)
}

// gitPaths checks pathspecs (after --, or positional for add/rm/mv).
func (s *state) gitPaths(sub string, args []arg) {
	if hasPrefixArg(args, "--output") {
		s.mut = true
	}
	dash := slices.IndexFunc(args, func(a arg) bool { return a.s == "--" })
	for j, a := range args[min(1, len(args)):] {
		if (dash >= 0 && j+1 > dash) || (dash < 0 && inList(" add rm mv ", sub) && !strings.HasPrefix(a.s, "-")) {
			s.operand(a, inList(" rm mv restore checkout ", sub))
		}
	}
}

// find classifies starting points, -delete/-fprint writes and -exec
// commands ({} stands for the first starting point).
func (s *state) find(args []arg) {
	expr := slices.IndexFunc(args, func(a arg) bool { return strings.HasPrefix(a.s, "-") || a.s == "(" || a.s == "!" })
	if expr < 0 {
		expr = len(args)
	}
	write := false
	for i := expr; i < len(args); i++ {
		switch args[i].s {
		case "-delete", "-fprint", "-fprint0", "-fprintf", "-fls":
			s.mut, write = true, true
		case "-exec", "-execdir", "-ok", "-okdir":
			s.mut = true
			end := i + 1
			for ; end < len(args) && args[end].s != ";" && args[end].s != "+"; end++ {
				if args[end].s == "{}" && expr > 0 {
					args[end] = args[0]
				}
			}
			if end > i+1 {
				s.nested(args[i+1:end], stdinNone)
			}
			i = end
		}
	}
	for _, a := range args[:expr] {
		s.operand(a, write)
	}
}
