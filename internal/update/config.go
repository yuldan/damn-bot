package update

import (
	"fmt"
	"net/url"
)

// Config is SPEC §12 [update] (ADR-216).
type Config struct {
	FeedURL     string `toml:"feed_url" json:"feed_url"`
	Channel     string `toml:"channel" json:"channel"`
	AutoInstall bool   `toml:"auto_install" json:"auto_install"`
}

// DefaultConfig returns the safe SPEC §12 defaults.
func DefaultConfig() Config { return Config{Channel: channelStable} }

// Validate checks the configured channel and feed URL.
func (c Config) Validate() error {
	if c.Channel != channelStable && c.Channel != channelBeta {
		return fmt.Errorf("update.channel must be %q or %q", channelStable, channelBeta)
	}
	if c.FeedURL == "" {
		return nil
	}
	u, err := url.Parse(c.FeedURL)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("update.feed_url must be absolute without userinfo")
	}
	if u.Scheme != "https" && !loopbackHTTP(u) {
		return fmt.Errorf("update.feed_url must be https (http only for loopback)")
	}
	return nil
}
