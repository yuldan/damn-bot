package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gateway/telegram"
)

const (
	telegramModePolling  = "polling"
	telegramModeWebhook  = "webhook"
	defaultWebhookListen = "127.0.0.1:7878"
	webhookStopTimeout   = 5 * time.Second
	webhookDeleteTimeout = 10 * time.Second
)

// telegramWebhook is the resolved webhook configuration of `serve`. Enabled
// is false for polling; mode is never both, which is what keeps polling and
// webhook mutually exclusive (fail-closed below).
type telegramWebhook struct {
	Enabled bool
	URL     string
	Listen  string
	Secret  string
}

// resolveWebhook validates the mode switch and the webhook settings. Webhook
// mode requires a secret and a public URL; polling mode rejects every
// webhook key, so a half-configured server never silently stays in polling.
func resolveWebhook(mode, listen, hookURL, secret string) (telegramWebhook, error) {
	switch strings.ToLower(strings.TrimSpace(cmp.Or(mode, telegramModePolling))) {
	case telegramModePolling:
		if listen != "" || hookURL != "" || secret != "" {
			return telegramWebhook{}, errors.New("gateway: telegram mode=polling conflicts with webhook settings (--telegram-webhook-listen/-url or DAMNBOT_TELEGRAM_WEBHOOK_SECRET)")
		}
		return telegramWebhook{}, nil
	case telegramModeWebhook:
		if !telegram.ValidWebhookSecret(secret) {
			return telegramWebhook{}, fmt.Errorf("gateway: webhook mode: %w", telegram.ErrWebhookSecret)
		}
		if strings.TrimSpace(hookURL) == "" {
			return telegramWebhook{}, fmt.Errorf("gateway: webhook mode: %w", telegram.ErrWebhookURL)
		}
		addr := cmp.Or(strings.TrimSpace(listen), defaultWebhookListen)
		if err := validateWebhookListen(addr); err != nil {
			return telegramWebhook{}, err
		}
		return telegramWebhook{Enabled: true, URL: hookURL, Listen: addr, Secret: secret}, nil
	default:
		return telegramWebhook{}, fmt.Errorf("gateway: unknown telegram mode %q (want polling or webhook)", mode)
	}
}

// validateWebhookListen keeps the listener on loopback: a public reverse
// proxy is the only supported exposure, as with the observability endpoints.
func validateWebhookListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("gateway: webhook listen %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("gateway: webhook listen %q is not loopback; use 127.0.0.1:<port> or [::1]:<port>", addr)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 0 || n > 65535 {
		return fmt.Errorf("gateway: webhook listen %q has an invalid port", addr)
	}
	return nil
}

// setupWebhook binds the loopback listener and registers the public URL. The
// listener is bound before setWebhook so no update can arrive at a closed
// port; a registration failure aborts startup instead of polling silently.
func (g *serveGateway) setupWebhook(ctx context.Context, o *serveOptions) error {
	cfg, err := resolveWebhook(o.telegramMode, o.telegramWebhookListen, o.telegramWebhookURL,
		strings.TrimSpace(os.Getenv("DAMNBOT_TELEGRAM_WEBHOOK_SECRET")))
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	handler, err := g.adapter.WebhookHandler(telegram.WebhookOptions{Secret: cfg.Secret, In: g.inbound})
	if err != nil {
		return fmt.Errorf("gateway: telegram webhook handler: %w", err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("gateway: telegram webhook listener: %w", err)
	}
	if serr := g.adapter.SetWebhook(ctx, cfg.URL, cfg.Secret); serr != nil {
		if cerr := ln.Close(); cerr != nil {
			sessionsPrintf(g.stderr, "gateway: webhook listener close: %s\n", cleanLine(cerr.Error()))
		}
		return fmt.Errorf("gateway: telegram webhook register: %w", serr)
	}
	g.webhook = &gatewayWebhook{g: g, ln: ln, listen: ln.Addr().String(), handler: handler, url: cfg.URL, secret: cfg.Secret}
	sessionsPrintf(g.stderr, "gateway: telegram webhook listening on %s\n", ln.Addr())
	return nil
}

// closeWebhook releases a bound listener that never reached a runner.
func (g *serveGateway) closeWebhook() {
	if g.webhook != nil {
		g.webhook.close()
	}
}

// gatewayWebhook is the registry adapter of webhook mode: it registers the
// URL, serves the authenticated endpoint and unregisters on a planned stop.
type gatewayWebhook struct {
	g       *serveGateway
	mu      sync.Mutex
	ln      net.Listener
	listen  string
	handler http.Handler
	url     string
	secret  string
}

// listener hands the pre-bound startup listener to the first Run and rebinds
// the same address after a pause closed it; http.Server.Serve closes l.
func (w *gatewayWebhook) listener(ctx context.Context) (net.Listener, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ln != nil {
		ln := w.ln
		w.ln = nil
		return ln, nil
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", w.listen)
	if err != nil {
		return nil, fmt.Errorf("gateway: telegram webhook relisten %s: %w", w.listen, err)
	}
	w.listen = ln.Addr().String()
	return ln, nil
}

// close releases a listener that never reached a runner.
func (w *gatewayWebhook) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ln == nil {
		return
	}
	if err := w.ln.Close(); err != nil {
		sessionsPrintf(w.g.stderr, "gateway: webhook listener close: %s\n", cleanLine(err.Error()))
	}
	w.ln = nil
}

func (w *gatewayWebhook) Run(ctx context.Context) error {
	ln, err := w.listener(ctx)
	if err != nil {
		return err
	}
	if serr := w.g.adapter.SetWebhook(ctx, w.url, w.secret); serr != nil {
		if cerr := ln.Close(); cerr != nil {
			sessionsPrintf(w.g.stderr, "gateway: webhook listener close: %s\n", cleanLine(cerr.Error()))
		}
		return fmt.Errorf("telegram webhook: %w", serr)
	}
	srv := &http.Server{
		Handler:           w.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case serr := <-errc:
		w.unregister(ctx)
		if errors.Is(serr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("telegram webhook server: %w", serr)
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webhookStopTimeout)
	defer cancel()
	err = srv.Shutdown(shutCtx)
	w.unregister(ctx)
	if err != nil {
		return fmt.Errorf("telegram webhook shutdown: %w", err)
	}
	return nil
}

func (w *gatewayWebhook) unregister(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), webhookDeleteTimeout)
	defer cancel()
	if err := w.g.adapter.DeleteWebhook(ctx); err != nil {
		sessionsPrintf(w.g.stderr, "gateway: telegram deleteWebhook: %s\n", cleanLine(err.Error()))
	}
}
