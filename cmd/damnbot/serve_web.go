//go:build !minimal

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/web"
)

// webAvailable reports whether this build links the web UI.
func webAvailable() bool { return true }

// registerWebFlag declares `--web`; the help text is the full surface.
func registerWebFlag(fs *flag.FlagSet, web *bool) {
	fs.BoolVar(web, "web", false, "serve the local web UI on [web].listen (SPEC §13.2)")
}

// startServeWeb opens the `[web]` listener and returns its stop function. The
// listener never shares an address with the Telegram webhook. This slice serves
// only /healthz; auth and the assets land in the next F8 slices.
func startServeWeb(ctx context.Context, cfg *config.Config, o *serveOptions, dataDir string, _ *ctxdb.Store, stdout, stderr io.Writer) (func(context.Context) error, error) {
	if cerr := webListenConflict(cfg.Web.Listen, o); cerr != nil {
		return nil, cerr
	}
	wc := web.Config{
		Listen:       cfg.Web.Listen,
		AllowRemote:  cfg.Web.AllowRemote,
		AllowedHosts: cfg.Web.AllowedHosts,
		TLSCert:      cfg.Web.TLSCert,
		TLSKey:       cfg.Web.TLSKey,
		DataDir:      dataDir,
	}
	srv, err := web.New(&wc)
	if err != nil {
		return nil, fmt.Errorf("web ui: %w", err)
	}
	ln, err := srv.Listen(ctx)
	if err != nil {
		return nil, fmt.Errorf("web ui: %w", err)
	}
	go func() {
		if serr := srv.Serve(ln); serr != nil {
			sessionsPrintf(stderr, "web ui: %s\n", cleanLine(serr.Error()))
		}
	}()
	sessionsPrintf(stdout, "web: listening on %s\n", srv.Addr())
	return srv.Shutdown, nil
}

// webListenConflict refuses to start two listeners of this process on one
// address. SPEC §13.2 pins the web UI at 127.0.0.1:7878 and the Telegram
// webhook flag defaults to the same address, so the operator must move one of
// them instead of silently sharing a port (fail-closed).
func webListenConflict(webAddr string, o *serveOptions) error {
	if o.telegramMode != "webhook" || o.telegramWebhookListen == "" {
		return nil
	}
	if sameListenAddr(webAddr, o.telegramWebhookListen) {
		return fmt.Errorf("web ui: listen %q collides with the Telegram webhook listener %q; set [web] listen or --telegram-webhook-listen to different addresses", webAddr, o.telegramWebhookListen)
	}
	return nil
}

// sameListenAddr reports a collision when the ports match and either host is a
// wildcard (empty, 0.0.0.0, ::) or both hosts are identical.
func sameListenAddr(a, b string) bool {
	ha, pa, ea := net.SplitHostPort(a)
	hb, pb, eb := net.SplitHostPort(b)
	if ea != nil || eb != nil || pa != pb {
		return false
	}
	return wildcardHost(ha) || wildcardHost(hb) || ha == hb
}

// wildcardHost is an all-interfaces bind.
func wildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}
