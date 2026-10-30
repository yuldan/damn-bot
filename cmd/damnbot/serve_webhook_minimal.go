//go:build minimal

package main

import (
	"context"
	"errors"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
)

// gatewayWebhook is absent from the minimal build; Telegram stays on polling.
type gatewayWebhook struct{}

func (*gatewayWebhook) Run(context.Context) error { return nil }

func (g *serveGateway) closeWebhook() {}

func (g *serveGateway) setupWebhook(_ context.Context, o *serveOptions) error {
	mode := strings.ToLower(strings.TrimSpace(o.telegramMode))
	secret := strings.TrimSpace(config.OS().Getenv("DAMNBOT_TELEGRAM_WEBHOOK_SECRET"))
	if mode == "" || mode == "polling" {
		if o.telegramWebhookListen != "" || o.telegramWebhookURL != "" || secret != "" {
			return errors.New("gateway: telegram mode=polling conflicts with webhook settings")
		}
		return nil
	}
	return errors.New("gateway: telegram webhook mode is unavailable in the minimal build")
}
