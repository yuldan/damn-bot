package exec

import "strings"

// sessionShell is §A.2's Windows session shell — pwsh, else powershell,
// else cmd — reading commands from a stdin pipe (ConPTY echo cannot be
// switched off, so the console would interleave the typed lines with the
// output; ADR-105 butir 22).
func sessionShell(path string) sessionShellSpec {
	c, err := chooseShell(path)
	if err != nil {
		return cmdSession("cmd.exe")
	}
	if c.kind == ShellPowerShell {
		return pwshSession(c.bin)
	}
	return cmdSession(c.bin)
}

func pwshSession(bin string) sessionShellSpec {
	sentinel := func(nonce, code string) string {
		return `[Console]::Out.Write("$([char]30)DB:` + nonce + `:` + code + `:$([char]30)")` + "\n"
	}
	writeCwd := func(cwd string) string {
		return "Set-Content -LiteralPath '" + strings.ReplaceAll(cwd, "'", "''") +
			"' -Value (Get-Location).ProviderPath; "
	}
	return sessionShellSpec{
		argv: []string{bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-"},
		ext:  ".ps1", setup: "$ProgressPreference = 'SilentlyContinue'",
		resetEnv: "$env:NoDefaultCurrentDirectoryInExePath='1';",
		line: func(path, cwd, nonce string) string {
			return "$global:LASTEXITCODE = 0; . '" + strings.ReplaceAll(path, "'", "''") + "'; " +
				writeCwd(cwd) + sentinel(nonce, "${LASTEXITCODE}")
		},
		again: func(nonce, cwd string) string {
			return writeCwd(cwd) + sentinel(nonce, "130")
		},
	}
}

func cmdSession(bin string) sessionShellSpec {
	// The sentinel carries only the nonce and the saved exit code: the cwd
	// goes to a file, so no `%cd%`/`!cd!` is ever expanded into a command
	// line (REVIEW-F1-SEC5 EX5-3). `cd` writes the provider path without
	// being reparsed; the saved %__dmb% survives the extra line.
	sentinel := func(nonce, code string) string {
		return "echo \x1eDB:" + nonce + ":" + code + ":\x1e\r\n"
	}
	return sessionShellSpec{
		argv: []string{bin, "/d", "/q"}, ext: ".cmd", setup: "rem",
		resetEnv: `set "NoDefaultCurrentDirectoryInExePath=1"` + "\r\n",
		line: func(path, cwd, nonce string) string {
			return `call "` + path + `"` + "\r\n" +
				`set "__dmb=%errorlevel%"` + "\r\n" +
				`cd > "` + cwd + `"` + "\r\n" +
				sentinel(nonce, "%__dmb%")
		},
		again: func(nonce, cwd string) string {
			return `cd > "` + cwd + `"` + "\r\n" + sentinel(nonce, "130")
		},
	}
}

// interruptForeground: a pipe session has no foreground job to signal;
// the shell is killed and recreated after the wait (§A.2 step 3).
func interruptForeground(*session) error { return nil }
