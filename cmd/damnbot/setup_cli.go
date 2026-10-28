package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
)

// setupCredential is one well-known environment credential. Only the name is
// ever printed; the value is never read into output (SPEC §4.9).
type setupCredential struct{ env, provider string }

func setupCredentials() []setupCredential {
	return []setupCredential{
		{"OPENAI_API_KEY", "openai"},
		{"ANTHROPIC_API_KEY", "anthropic"},
		{"OPENROUTER_API_KEY", "openrouter"},
		{"GEMINI_API_KEY", "gemini"},
		{"GOOGLE_API_KEY", "google"},
		{"GROQ_API_KEY", "groq"},
		{"MISTRAL_API_KEY", "mistral"},
		{"DEEPSEEK_API_KEY", "deepseek"},
		{"XAI_API_KEY", "xai"},
	}
}

func setupDispatch(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "completion":
		return true, setupCompletion(args[1:], stdout, stderr)
	case "secrets":
		return true, setupSecrets(args[1:], stdout, stderr)
	}
	return false, 0
}

// runSetup implements the ≤3-step wizard (SPEC §10, ROADMAP F3 acceptance).
func runSetup(args []string, stdout, stderr io.Writer) int {
	if handled, code := setupDispatch(args, stdout, stderr); handled {
		return code
	}
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	yes := flags.Bool("yes", false, "non-interactive (choose --model)")
	model := flags.String("model", "", "provider/model to write")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		say(stderr, "setup: %q is not available in F3\n", cleanArg(flags.Arg(0)))
		return 2
	}
	if !*yes && !configTerminal(stdout) {
		say(stderr, "setup: not a terminal; pass --yes with --model provider/model\n")
		return 2
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return setupFail(stderr, err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return setupFail(stderr, err)
	}
	file := paths.ConfigFile()
	cfg, err := loader.Load(file)
	exists := true
	if errors.Is(err, fs.ErrNotExist) {
		cfg, exists = config.Default(), false
	} else if err != nil {
		return setupFail(stderr, fmt.Errorf("existing config %s is invalid, not overwritten: %w", file, err))
	}
	if code := say(stdout, "1/3 credentials: "); code != 0 {
		return code
	}
	printSetupCredentials(stdout)
	if code, done := setupExisting(stdout, stderr, file, &cfg, exists, *yes); done {
		return code
	}
	if code := setupChooseModel(stdin(), stdout, stderr, *yes, model); code != 0 {
		return code
	}
	return setupWrite(loader, paths, file, *model, stdout, stderr)
}

// setupExisting handles the valid-config branch and returns done=true when
// the wizard must stop (keep/unchanged or a non-TTY refusal).
func setupExisting(stdout, stderr io.Writer, file string, cfg *config.Config, exists, yes bool) (code int, done bool) {
	if !exists || cfg.Provider.Model == "" {
		return 0, false
	}
	say(stdout, "current model: %s (%s)\n", cfg.Provider.Model, file)
	if yes {
		say(stdout, "3/3 unchanged: %s\n", file)
		return 0, true
	}
	if !configTerminal(stdout) {
		say(stderr, "setup: not a terminal; pass --yes to keep the current config\n")
		return 2, true
	}
	keep, kerr := promptYes(stdin(), stdout, "keep current model? [Y/n] ")
	if kerr != nil {
		return setupFail(stderr, kerr), true
	}
	if keep {
		say(stdout, "3/3 unchanged: %s\n", file)
		return 0, true
	}
	return 0, false
}

func setupChooseModel(in io.Reader, stdout, stderr io.Writer, yes bool, model *string) int {
	if *model != "" {
		if !validModelRef(*model) {
			say(stderr, "setup: --model must be provider/model\n")
			return 2
		}
		return 0
	}
	if yes || !configTerminal(stdout) {
		say(stderr, "setup: no --model; detected credentials above (secret values are never printed)\n")
		return 2
	}
	line, err := promptLine(in, stdout, "model (provider/model): ")
	if err != nil {
		return setupFail(stderr, err)
	}
	*model = strings.TrimSpace(line)
	if !validModelRef(*model) {
		say(stderr, "setup: model must be provider/model\n")
		return 2
	}
	return 0
}

func setupWrite(loader *config.Loader, paths config.Paths, file, model string, stdout, stderr io.Writer) int {
	body := "[provider]\nmodel = " + strconv.Quote(model) + "\n"
	if _, err := loader.Parse([]byte(body), file); err != nil {
		return setupFail(stderr, err)
	}
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		return setupFail(stderr, err)
	}
	tmp, err := os.CreateTemp(paths.ConfigDir, ".config.toml.setup-*")
	if err != nil {
		return setupFail(stderr, err)
	}
	name := tmp.Name()
	defer func() {
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return
		}
	}()
	if _, werr := tmp.WriteString(body); werr != nil {
		return setupFail(stderr, werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return setupFail(stderr, cerr)
	}
	if err := commitConfig(name, file); err != nil {
		return setupFail(stderr, err)
	}
	say(stdout, "2/3 model: %s\n3/3 wrote %s (2 lines); next: damnbot doctor\n", model, file)
	return 0
}

func stdin() io.Reader { return os.Stdin }

func setupFail(stderr io.Writer, err error) int {
	say(stderr, "setup: %s\n", cleanLine(err.Error()))
	return 1
}

// printSetupCredentials names detected providers without reading values into
// any output path: only os.Getenv != "" is observed.
func printSetupCredentials(w io.Writer) {
	found := 0
	for _, c := range setupCredentials() {
		if os.Getenv(c.env) == "" {
			continue
		}
		found++
		if code := say(w, "%s → %s ", c.env, c.provider); code != 0 {
			return
		}
	}
	if found == 0 {
		say(w, "none (set a provider key or pass --model)\n")
		return
	}
	say(w, "\n")
}

func validModelRef(ref string) bool {
	prov, model, ok := strings.Cut(ref, "/")
	return ok && prov != "" && model != "" && !strings.ContainsAny(ref, "\x00\n\r")
}

func promptYes(in io.Reader, out io.Writer, prompt string) (bool, error) {
	line, err := promptLine(in, out, prompt)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, errors.New("answer y or n")
	}
}

func promptLine(in io.Reader, out io.Writer, prompt string) (string, error) {
	if code := say(out, "%s", prompt); code != 0 {
		return "", errors.New("write prompt")
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read answer: %w", err)
	}
	return line, nil
}

func setupUsage(w io.Writer) {
	say(w, "usage: damnbot setup [--yes] [--model provider/model]\n       damnbot setup completion bash|zsh|fish|powershell\n       damnbot setup secrets set|rotate [--all-profiles]|ls [--refs]\n")
}
