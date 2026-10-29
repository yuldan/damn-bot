package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/obs"
)

// Exit codes of `damnbot serve` for supervisors (SPEC §3.3, SELF-HEALING §1.2).
const (
	serveExitClean    = 0
	serveExitSoftware = 70
	serveExitPlanned  = 75
	serveExitFatal    = 78
)

const (
	serveDrainDefault = 30 * time.Second
	serveBeatPeriod   = time.Second
	serveShutdownMax  = 5 * time.Second
)

// serveOptions is the flag set of the daemon. This slice opens only the
// observability listener; web/API/gateway/cron land in later F4 slices.
type serveOptions struct {
	bind         string
	configPath   string
	dataDir      string
	drainTimeout time.Duration
}

// serveFlagSet defines the flags once for `serve`, its help and tests.
func serveFlagSet(o *serveOptions, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.bind, "bind", obs.DefaultListen, "observability listener (loopback IP:port; SPEC §15 default 127.0.0.1:7879)")
	fs.StringVar(&o.configPath, "config", "", "config.toml path (default: profile path)")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default: SPEC §11)")
	fs.DurationVar(&o.drainTimeout, "drain-timeout", serveDrainDefault, "graceful stop window for active turns before they are cancelled")
	return fs
}

// serveUsage is `damnbot help serve` / `damnbot serve --help`.
func serveUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot serve [flags]\n")
	serveFlagSet(&serveOptions{}, w).PrintDefaults()
}

// runServe is the surface entry point (SPEC §10: `serve`).
func runServe(args []string, stdout, stderr io.Writer) int {
	var o serveOptions
	fs := serveFlagSet(&o, stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		sessionsPrintf(stderr, "serve: unexpected arguments: %v\n", fs.Args())
		return 2
	}
	code, err := serveRun(context.Background(), &o, stdout, stderr)
	if err != nil {
		sessionsPrintf(stderr, "serve: %s\n", cleanLine(err.Error()))
	}
	return code
}

// serveRun is the daemon lifecycle: single-serve lease, DB, loopback
// observability, signal drain, clean close. It opens no outbound connection,
// so [network] offline/background policy (netx) holds by construction.
func serveRun(ctx context.Context, o *serveOptions, stdout, stderr io.Writer) (int, error) {
	if err := obs.ValidateListen(o.bind); err != nil {
		return serveExitFatal, fmt.Errorf("observability listener: %w", err)
	}
	if o.drainTimeout <= 0 {
		return serveExitFatal, errors.New("--drain-timeout must be positive")
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return serveExitFatal, fmt.Errorf("config deps: %w", err)
	}
	if _, lerr := loadRunConfig(loader, o.configPath); lerr != nil {
		return serveExitFatal, lerr
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return serveExitFatal, fmt.Errorf("config paths: %w", err)
	}
	dataDir := cmp.Or(o.dataDir, paths.DataDir)
	lock, err := acquireServeLock(dataDir)
	if err != nil {
		return serveExitFatal, err // GAP-REL-03: second instance exits 78 with the PID
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			sessionsPrintf(stderr, "serve: release data-dir lease: %s\n", cleanLine(rerr.Error()))
		}
	}()

	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "serve"})
	if err != nil {
		if errors.Is(err, ctxdb.ErrSchemaNewer) {
			return serveExitPlanned, fmt.Errorf("open ctxdb: %w", err)
		}
		return serveExitFatal, fmt.Errorf("open ctxdb: %w", err)
	}
	defer func() {
		if cerr := db.CloseContext(context.WithoutCancel(ctx)); cerr != nil {
			sessionsPrintf(stderr, "serve: close ctxdb: %s\n", cleanLine(cerr.Error()))
		}
	}()

	metrics := obs.NewMetrics()
	metrics.SetLeaseHeld(true)
	metrics.SetQueue(0)
	eps := obs.New(obs.Config{
		Metrics: metrics,
		Checks: []obs.Check{
			{Name: "db", Fn: db.ProbeWrite},
			{Name: "serve_lock", Fn: func(context.Context) error {
				if !lock.Held() {
					return errors.New("data-dir lease is not held")
				}
				return nil
			}},
		},
	})
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", o.bind)
	if err != nil {
		return serveExitFatal, fmt.Errorf("observability listener: %w", err)
	}
	hs := &http.Server{Handler: eps, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	errs := obs.Serve(hs, ln)
	sessionsPrintf(stderr, "serve: data dir %s\n", dataDir)
	sessionsPrintf(stdout, "serve: observability listening on %s\n", ln.Addr())
	return serveLoop(ctx, o, eps, hs, errs, db, lock, stderr)
}

// serveLoop waits for a stop signal (or a fatal endpoint error), drains the
// active turns, shuts the listener down and closes the store.
func serveLoop(ctx context.Context, o *serveOptions, eps *obs.Endpoints, hs *http.Server, errs <-chan error, db *ctxdb.Store, lock *serveLock, stderr io.Writer) (int, error) {
	daemon := newServeDaemon()
	defer daemon.Close()
	stopBeat := eps.StartBeatLoop(ctx, serveBeatPeriod, lock.Held)
	defer stopBeat()
	registerServeTestTurn(daemon, stderr)

	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	var serveErr error
	select {
	case <-sigCtx.Done():
		sessionsPrintf(stderr, "serve: stop requested; draining\n")
	case serveErr = <-errs:
	}

	if !daemon.Drain(o.drainTimeout) {
		sessionsPrintf(stderr, "serve: drain window %s elapsed; active turns cancelled\n", o.drainTimeout)
	}
	daemon.Close()
	shutCtx, cancelShut := context.WithTimeout(context.WithoutCancel(ctx), serveShutdownMax)
	shutErr := hs.Shutdown(shutCtx)
	cancelShut()
	closeErr := db.CloseContext(context.WithoutCancel(ctx))
	switch {
	case serveErr != nil:
		return serveExitSoftware, fmt.Errorf("observability server: %w", serveErr)
	case shutErr != nil:
		return serveExitSoftware, fmt.Errorf("shutdown observability server: %w", shutErr)
	case closeErr != nil:
		return serveExitSoftware, fmt.Errorf("close ctxdb: %w", closeErr)
	}
	return serveExitClean, nil
}
