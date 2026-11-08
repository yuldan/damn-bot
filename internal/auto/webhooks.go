//go:build !minimal

package auto

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// Sender is the injected egress seam. The production adapter uses the
// already-registered netx client "webhook-delivery"; this package never dials
// itself, so the netx registry and its budget do not grow.
type Sender interface {
	Send(ctx context.Context, target string, headers map[string]string, body []byte) error
}

// WebhookConfig is one outbound target. Secret signs the body.
type WebhookConfig struct {
	URL    string
	Secret string
}

// SignatureHeader carries the hex HMAC-SHA256 of the body.
const SignatureHeader = "X-Damnbot-Signature"

// SendWebhook delivers one event for AUTO-032. A disabled feature returns
// [ErrDisabled] without touching the sender; the URL must be https or a
// loopback http target, and the body is always signed.
func SendWebhook(ctx context.Context, s *Service, sender Sender, cfg WebhookConfig, body []byte) error {
	if !s.Enabled(OutboundWebhook) {
		return fmt.Errorf("%w: %s", ErrDisabled, OutboundWebhook)
	}
	if sender == nil {
		return fmt.Errorf("%w: %s sender", ErrNoStore, OutboundWebhook)
	}
	if err := validateWebhookURL(cfg.URL); err != nil {
		return err
	}
	if cfg.Secret == "" {
		return fmt.Errorf("%w: webhook secret is empty", ErrConfig)
	}
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	_, _ = mac.Write(body)
	headers := map[string]string{
		"Content-Type":  "application/json",
		SignatureHeader: hex.EncodeToString(mac.Sum(nil)),
	}
	if err := sender.Send(ctx, cfg.URL, headers, body); err != nil {
		return fmt.Errorf("auto: %s send: %w", OutboundWebhook, err)
	}
	return nil
}

func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: webhook url is invalid", ErrConfig)
	}
	if u.Scheme == "https" {
		return nil
	}
	host := strings.Trim(u.Hostname(), "[]")
	if u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return nil
	}
	return fmt.Errorf("%w: webhook url must be https or loopback http", ErrConfig)
}
