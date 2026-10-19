package exec

import "testing"

func TestPromptKind(t *testing.T) {
	t.Parallel()
	rules := newPromptRules()
	for line, want := range map[string]string{
		"[sudo] password for dev: ":                            InputSecret,
		"Enter passphrase for key '/home/u/.ssh/id_ed25519': ": InputSecret,
		"Password:":                       InputSecret,
		"Paste the token here: ":          InputSecret,
		"Do you want to continue? [Y/n] ": InputConfirm,
		"Overwrite? (y/n)":                InputConfirm,
		"Proceed [yes/no]: ":              InputConfirm,
		"Are you sure you want to continue connecting (yes/no/[fingerprint])? ": InputConfirm,
		"(END)":                           InputPager,
		"--More--(42%)":                   InputPager,
		":":                               InputPager,
		"Press ENTER to continue":         InputText,
		"Username for 'https://x.test': ": InputText,
		"package name: (app) ":            "", // no pattern: Linux wchan after 5 s
		"What is your name? ":             InputText,
		"Building 3/7":                    "",
		"compiling foo.c":                 "",
		"":                                "",
	} {
		if got := promptKind(rules, line); got != want {
			t.Errorf("%q: %q, want %q", line, got, want)
		}
	}
}

func TestLastLine(t *testing.T) {
	t.Parallel()
	long := make([]byte, maxPromptLine+1)
	for i := range long {
		long[i] = 'x'
	}
	for tail, want := range map[string]string{
		"a\nb\nName: ":                   "Name: ",
		"done\n":                         "",
		"50%\rReplace? [y/n] ":           "Replace? [y/n] ",
		"\x1b[1mContinue? [y/N]\x1b[0m ": "Continue? [y/N] ",
		"\x1b]0;title\x07Password: ":     "Password: ",
		"line\n" + string(long):          "",
		"progress 10%\rprogress 20%\r":   "progress 20%",
	} {
		if got := lastLine([]byte(tail)); got != want {
			t.Errorf("%q: %q, want %q", tail, got, want)
		}
	}
}
