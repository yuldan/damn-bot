//go:build !minimal

package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"runtime"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/doctor"
	"github.com/damnbot/damnbot/internal/netx"
)

// runDoctorToolsFetch implements `doctor tools fetch <name> [--yes]`
// (GAP-TOOL-06). It displays the license, refuses without --yes, and installs
// only the artifact pinned in the embedded lock. The downloader is created
// here so the same netx Tool lane (anti-SSRF dial rule + DLP) carries the
// request.
func runDoctorToolsFetch(parsed doctorToolsArgs, stdout, stderr io.Writer) int {
	if len(parsed.positional) != 1 {
		doctorToolsUsage(stderr)
		return 2
	}
	name := parsed.name
	lock, err := doctor.EmbeddedToolsLock()
	if err != nil {
		say(stderr, "doctor tools: %s\n", cleanLine(err.Error()))
		return 1
	}
	p, ok := lock.Lookup(name)
	if !ok {
		say(stderr, "doctor tools: %q is not in the lock (lock lists %s)\n", cleanArg(name), cleanLine(strings.Join(lock.Names(), ", ")))
		return 1
	}
	root, err := doctorDataDir()
	if err != nil {
		say(stderr, "doctor tools: %s\n", cleanLine(err.Error()))
		return 1
	}
	say(stdout, "%s %s · license %s · %s\n", p.Name, p.Version, p.License, p.LicenseURL)
	if !parsed.yes {
		say(stderr, "doctor tools fetch: %s is %s; review the license, then re-run with --yes\n", p.Name, p.License)
	}
	st, ferr := doctor.FetchTool(context.Background(), lock, &doctor.FetchOptions{
		Root: root, Name: name, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		AcceptLicense: parsed.yes, Downloader: doctorDownloader(),
	})
	if ferr != nil {
		say(stderr, "doctor tools fetch: %s\n", cleanLine(ferr.Error()))
		return 1
	}
	return printToolStatuses([]doctor.ToolStatus{st}, parsed.jsonOut, stdout, stderr)
}

// doctorDownloader builds the egress-gated downloader. A pinned URL is the
// only input; the Tool purpose keeps the anti-SSRF dial rule and DLP gate.
func doctorDownloader() doctor.Downloader {
	loader, err := config.New(config.OS())
	if err != nil {
		return doctor.HTTPDownloader{}
	}
	paths, perr := loader.Paths(activeProfile())
	cfg, cerr := loader.Load(paths.ConfigFile())
	if perr != nil || cerr != nil || errors.Is(cerr, fs.ErrNotExist) {
		cfg = config.Default()
	}
	_ = perr
	network := netx.New(netx.Config{Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background})
	return doctor.HTTPDownloader{Client: network.Client("doctor-tools-fetch", netx.Tool)}
}
