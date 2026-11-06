//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/gwplat/webhook"
)

// envSecret serves exactly the receiver's declared secret from the process
// environment; the value never enters a flag, argv or log line.
type envSecret string

func (s envSecret) Get(_ context.Context, name string) (string, error) {
	if name != "webhook_secret" || s == "" {
		return "", errors.New("webhook secret is not configured")
	}
	return string(s), nil
}

// startGenericWebhook starts the opt-in generic receiver (PLAT-029). It is
// disabled unless DAMNBOT_WEBHOOK_LISTEN is set; the listener must be
// loopback and the shared secret must be present, otherwise serve refuses to
// start the surface.
func (g *serveGateway) startGenericWebhook(ctx context.Context) error {
	addr := strings.TrimSpace(config.OS().Getenv("DAMNBOT_WEBHOOK_LISTEN"))
	if addr == "" {
		return nil
	}
	secretValue := strings.TrimSpace(config.OS().Getenv("DAMNBOT_WEBHOOK_SECRET"))
	if secretValue == "" {
		return errors.New("gateway: DAMNBOT_WEBHOOK_LISTEN requires DAMNBOT_WEBHOOK_SECRET")
	}
	if err := requireLoopbackAddr(addr); err != nil {
		return err
	}
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		return fmt.Errorf("gateway: webhook registry: %w", err)
	}
	desc, err := reg.Lookup("webhook")
	if err != nil {
		return fmt.Errorf("gateway: webhook descriptor: %w", err)
	}
	adapter, err := webhook.New(ctx, &webhook.Config{Enabled: true, Log: slog.Default()},
		&desc, envSecret(secretValue), nil, gwplat.HandlerFunc(g.webhookInbound))
	if err != nil {
		return fmt.Errorf("gateway: webhook receiver: %w", err)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("gateway: webhook listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/webhook", adapter)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	g.webhookSrv = srv
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if serveErr := srv.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			sessionsPrintf(g.stderr, "gateway: webhook server: %s\n", cleanLine(serveErr.Error()))
		}
	}()
	return nil
}

// webhookInbound hands one admitted delivery to the same per-chat lane as a
// platform update. The webhook toolset is pinned by config (TSET-061), never
// by the payload.
func (g *serveGateway) webhookInbound(_ context.Context, in *gateway.Inbound) error {
	// The HTTP request context ends when ServeHTTP returns; the lane must live
	// with the daemon, so dispatch on the serve run context.
	ctx := g.webhookCtx
	if ctx == nil {
		ctx = context.Background()
	}
	g.dispatch(ctx, &telegram.Inbound{
		Platform: in.Platform, ChatID: in.ChatID, ThreadID: in.ThreadID,
		SenderID: in.SenderID, UpdateID: in.UpdateID, Text: in.Text,
		Group: in.Group, Mentioned: true,
	})
	return nil
}

func (g *serveGateway) stopGenericWebhook(ctx context.Context) {
	if g == nil || g.webhookSrv == nil {
		return
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, serveShutdownMax)
	defer cancel()
	if err := g.webhookSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		sessionsPrintf(g.stderr, "gateway: stop webhook server: %s\n", cleanLine(err.Error()))
	}
}

// requireLoopbackAddr rejects a listener that would expose an unauthenticated
// callback surface on a non-loopback interface.
func requireLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("gateway: webhook listen %q: %w", cleanArg(addr), err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("gateway: webhook listen %q must be loopback", cleanArg(addr))
	}
	return nil
}
