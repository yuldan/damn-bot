package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gateway"
)

func runPause(args []string, stdout, stderr io.Writer) int {
	return runEstop(args, true, stdout, stderr)
}

func runResume(args []string, stdout, stderr io.Writer) int {
	return runEstop(args, false, stdout, stderr)
}

func runEstop(args []string, on bool, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("estop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) > 1 || (len(pos) == 1 && !on) {
		if on {
			pauseUsage(stderr)
		} else {
			resumeUsage(stderr)
		}
		return 2
	}
	reason := ""
	if len(pos) == 1 {
		reason = pos[0]
	}
	dir, err := estopDataDir(*dataDir)
	if err != nil {
		sessionsPrintf(stderr, "emergency stop: %s\n", cleanLine(err.Error()))
		return 1
	}
	stop := gateway.Estop{Dir: dir}
	if on {
		st, perr := stop.Pause(context.Background(), reason)
		if perr != nil {
			sessionsPrintf(stderr, "pause: %s\n", cleanLine(perr.Error()))
			return 1
		}
		sessionsPrintf(stdout, "emergency stop on: new gateway turns and cron dispatch are stopped (%s); messages are parked until `damnbot resume`\n", cleanLine(st.Reason))
		return 0
	}
	if _, rerr := stop.Resume(context.Background()); rerr != nil {
		sessionsPrintf(stderr, "resume: %s\n", cleanLine(rerr.Error()))
		return 1
	}
	sessionsPrintf(stdout, "emergency stop off: parked gateway messages and due cron slots resume\n")
	return 0
}

func estopDataDir(dir string) (string, error) {
	if dir == "" {
		loader, err := config.New(config.OS())
		if err != nil {
			return "", fmt.Errorf("config deps: %w", err)
		}
		paths, err := loader.Paths(activeProfile())
		if err != nil {
			return "", fmt.Errorf("config paths: %w", err)
		}
		dir = paths.DataDir
	}
	return resolveDataDir(dir)
}

func pauseUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot pause [reason] [--data-dir DIR]\nstops new gateway turns and cron dispatch\n")
}

func resumeUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot resume [--data-dir DIR]\nlifts the emergency stop set by `damnbot pause`\n")
}
