package main

import "testing"

func TestResolveWebhook(t *testing.T) {
	cases := []struct {
		name, mode, listen, url, secret string
		enabled                         bool
		wantListen                      string
		ok                              bool
	}{
		{name: "default is polling", ok: true},
		{name: "explicit polling", mode: "polling", ok: true},
		{name: "polling with listener", mode: "polling", listen: "127.0.0.1:9000"},
		{name: "polling with url", mode: "polling", url: "https://x.test/h"},
		{name: "polling with secret", mode: "polling", secret: "s3cret"},
		{name: "webhook default listen", mode: "webhook", url: "https://x.test/h", secret: "s3cret", enabled: true, wantListen: defaultWebhookListen, ok: true},
		{name: "webhook custom listen", mode: "WEBHOOK", listen: "127.0.0.1:9000", url: "https://x.test/h", secret: "s3cret", enabled: true, wantListen: "127.0.0.1:9000", ok: true},
		{name: "webhook missing secret", mode: "webhook", url: "https://x.test/h"},
		{name: "webhook weak secret", mode: "webhook", url: "https://x.test/h", secret: "not a secret"},
		{name: "webhook missing url", mode: "webhook", secret: "s3cret"},
		{name: "webhook non-loopback", mode: "webhook", listen: "0.0.0.0:9000", url: "https://x.test/h", secret: "s3cret"},
		{name: "unknown mode", mode: "long-poll", url: "https://x.test/h", secret: "s3cret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWebhook(tc.mode, tc.listen, tc.url, tc.secret)
			if (err == nil) != tc.ok {
				t.Fatalf("error = %v, want ok=%t", err, tc.ok)
			}
			if err != nil {
				return
			}
			if got.Enabled != tc.enabled {
				t.Errorf("enabled = %t, want %t", got.Enabled, tc.enabled)
			}
			if tc.enabled && got.Listen != tc.wantListen {
				t.Errorf("listen = %q, want %q", got.Listen, tc.wantListen)
			}
			if tc.enabled && (got.URL != tc.url || got.Secret != tc.secret) {
				t.Errorf("config = %+v", got)
			}
		})
	}
}
