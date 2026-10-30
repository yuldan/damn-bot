package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/hooks"
)

func hooksDir(configPath string) (string, error) {
	if configPath != "" {
		return filepath.Join(filepath.Dir(configPath), "hooks"), nil
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return filepath.Join(paths.ConfigDir, "hooks"), nil
}

func configHooks(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, "usage: damnbot config hooks ls\n       damnbot config hooks test <name>\n")
		return 2
	}
	switch args[0] {
	case "ls", "list":
		return configHooksList(args[1:], stdout, stderr)
	case "test":
		return configHooksTest(args[1:], stdout, stderr)
	}
	say(stderr, "config hooks: unknown subcommand %q\nusage: damnbot config hooks ls|test <name>\n", cleanArg(args[0]))
	return 2
}

func configHooksList(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		say(stderr, "config hooks ls: unexpected argument %q\n", cleanArg(args[0]))
		return 2
	}
	dir, err := hooksDir("")
	if err != nil {
		return configFail(stderr, "config hooks ls", err)
	}
	entries, err := hooks.New(dir).List()
	if err != nil {
		return configFail(stderr, "config hooks ls", err)
	}
	if len(entries) == 0 {
		return say(stdout, "no hooks in %s\n", dir)
	}
	for _, h := range entries {
		if code := say(stdout, "%s\t%s\n", h.Name, cmp.Or(h.Problem, "ok")); code != 0 {
			return code
		}
	}
	return 0
}

func configHooksTest(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		say(stderr, "usage: damnbot config hooks test <name>\n")
		return 2
	}
	dir, err := hooksDir("")
	if err != nil {
		return configFail(stderr, "config hooks test", err)
	}
	reg := hooks.New(dir)
	if _, err := reg.Lookup(args[0]); err != nil {
		if errors.Is(err, hooks.ErrInvalidName) {
			say(stderr, "config hooks test: %s\n", cleanLine(err.Error()))
			return 2
		}
		return configFail(stderr, "config hooks test", err)
	}
	res, runErr := reg.Run(context.Background(), args[0], hooks.EventGatewayInbound, map[string]any{
		"platform": "test", "chat_id": "0", "user_id": "0", "text": "config hooks test",
	})
	if runErr != nil {
		say(stderr, "config hooks test: %s\n", cleanLine(runErr.Error()))
	}
	extra := ""
	if res.Truncated {
		extra += "truncated\ttrue\n"
	}
	if res.Stdout != "" {
		extra += "stdout:\n" + res.Stdout + "\n"
	}
	if res.Stderr != "" {
		extra += "stderr:\n" + res.Stderr + "\n"
	}
	code := say(stdout, "hook\t%s\nevent\t%s\ndecision\t%s\nexit\t%d\n%s",
		res.Name, hooks.EventGatewayInbound, res.Decision, res.ExitCode, extra)
	switch {
	case code != 0:
		return code
	case runErr != nil:
		return 1
	}
	return 0
}
