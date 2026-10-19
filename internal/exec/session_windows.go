package exec

import "strings"

// sessionShell is §A.2's Windows session shell — pwsh, else powershell,
// else cmd — reading commands from a stdin pipe (ConPTY echo cannot be
// switched off, so the console would interleave the typed lines with the
// output; ADR-105 butir 22).
func sessionShell(path string) sessionShellSpec {
	for _, name := range []string{"pwsh", "powershell"} {
		if p, err := lookPath(name, path, ""); err == nil {
			return pwshSession(p)
		}
	}
	p, err := lookPath("cmd", path, "")
	if err != nil {
		p = "cmd.exe"
	}
	return cmdSession(p)
}

func pwshSession(bin string) sessionShellSpec {
	sentinel := func(nonce, code string) string {
		return `[Console]::Out.Write("$([char]30)DB:` + nonce + `:` + code + `:$((Get-Location).ProviderPath)$([char]30)")` + "\n"
	}
	return sessionShellSpec{
		argv: []string{bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-"},
		ext:  ".ps1", setup: "$ProgressPreference = 'SilentlyContinue'",
		line: func(path, nonce string) string {
			return "$global:LASTEXITCODE = 0; . '" + strings.ReplaceAll(path, "'", "''") + "'; " + sentinel(nonce, "${LASTEXITCODE}")
		},
		again: func(nonce string) string { return sentinel(nonce, "130") },
	}
}

func cmdSession(bin string) sessionShellSpec {
	// %errorlevel% is expanded when its line is read, so the sentinel is a
	// line of its own, read after the call returned.
	sentinel := func(nonce, code string) string {
		return "echo \x1eDB:" + nonce + ":" + code + ":%cd%\x1e\r\n"
	}
	return sessionShellSpec{
		argv: []string{bin, "/d", "/q"}, ext: ".cmd", setup: "rem",
		line: func(path, nonce string) string {
			return `call "` + path + `"` + "\r\n" + sentinel(nonce, "%errorlevel%")
		},
		again: func(nonce string) string { return sentinel(nonce, "130") },
	}
}

// interruptForeground: a pipe session has no foreground job to signal;
// the shell is killed and recreated after the wait (§A.2 step 3).
func interruptForeground(*session) error { return nil }
