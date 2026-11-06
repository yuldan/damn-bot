// Package sms is the Twilio SMS adapter of F7 PLAT-020 and GAP-GW-09.
package sms

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- Twilio signs with HMAC-SHA1; protocol requirement.
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// GAP-GW-09 limits and prices.
const (
	SMSMaxSegments    = 3
	SMSMoreHint       = "balas MORE"
	DefaultCostMicros = 7900
	SMSEncodingGSM7   = "GSM-7"
	SMSEncodingUCS2   = "UCS-2"
)

// ErrConfig rejects invalid activation data.
var ErrConfig = errors.New("sms: invalid config")

// CostSink records billed segments.
type CostSink interface {
	RecordChannelCost(ctx context.Context, platform, chat string, segments int, micros int64) error
}

// Config is explicit activation; the zero value is disabled.
type Config struct {
	Enabled              bool
	API                  string
	HTTPClient           *http.Client
	Now                  func() time.Time
	Cost                 CostSink
	CostPerSegmentMicros int64
}

// Adapter is one Twilio account.
type Adapter struct {
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	sid     string
	token   string
	from    string
	dedupe  *gwplat.Dedupe
	now     func() time.Time
	http    gwplat.HTTP
	api     string
	cost    CostSink
	price   int64
}

// New validates explicit config and secrets; it opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "sms" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("sms: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("sms: secret: %w", err)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTPClient
	if client == nil {
		client = netx.LoopbackClient()
	}
	price := cfg.CostPerSegmentMicros
	if price <= 0 {
		price = DefaultCostMicros
	}
	api := cfg.API
	if api == "" {
		api = "https://api.twilio.com"
	}
	return &Adapter{
		desc: desc, out: out, handler: h, sid: values["sms_account_sid"],
		token: values["sms_auth_token"], from: values["sms_from"],
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute), now: now,
		http: gwplat.HTTP{Client: client}, api: strings.TrimRight(api, "/"),
		cost: cfg.Cost, price: price,
	}, nil
}

// Send caps one reply at three segments and records the billed cost.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	body, _, _, _ := FitSMS(markdown, SMSMaxSegments, "\n"+SMSMoreHint)
	err := a.out.Reply(ctx, gwplat.Reply{
		Chat: chat, Markdown: body,
		Send: func(ctx context.Context, part string) error {
			text, segments, _, _ := FitSMS(part, SMSMaxSegments, "\n"+SMSMoreHint)
			if a.cost != nil {
				if err := a.cost.RecordChannelCost(ctx, "sms", chat, segments, int64(segments)*a.price); err != nil {
					return fmt.Errorf("sms: ledger: %w", err)
				}
			}
			form := url.Values{"From": {a.from}, "To": {chat}, "Body": {text}}
			auth := base64.StdEncoding.EncodeToString([]byte(a.sid + ":" + a.token))
			_, err := a.http.Do(ctx, http.MethodPost,
				a.api+"/2010-04-01/Accounts/"+url.PathEscape(a.sid)+"/Messages.json", []byte(form.Encode()),
				map[string]string{"Authorization": "Basic " + auth, "Content-Type": "application/x-www-form-urlencoded"})
			if err != nil {
				return fmt.Errorf("sms: %w", err)
			}
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("sms: outbox: %w", err)
	}
	return nil
}

// Handler verifies X-Twilio-Signature over URL and form body.
func (a *Adapter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := a.http.ReadBody(w, r)
		if err != nil {
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil || !gwplat.ConstantTimeEqual(twilioSignature(a.token, gwplat.ExternalURL(r), form),
			r.Header.Get("X-Twilio-Signature")) {
			gwplat.Deny(w, http.StatusUnauthorized)
			return
		}
		sid, from := form.Get("MessageSid"), form.Get("From")
		if sid == "" || from == "" {
			gwplat.Deny(w, http.StatusBadRequest)
			return
		}
		in := gateway.Inbound{
			Platform: "sms", ChatID: from, SenderID: from, Text: form.Get("Body"),
			UpdateID: sid, Mentioned: true, Payload: string(body),
		}
		if !a.dedupe.Claim("sms:"+sid, a.now()) {
			w.WriteHeader(http.StatusOK)
			return
		}
		if derr := gwplat.Dispatch(r.Context(), a.handler, &in); derr != nil {
			gwplat.Deny(w, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/xml")
		if _, werr := w.Write([]byte("<Response/>")); werr != nil {
			return
		}
	})
}

func twilioSignature(token, rawURL string, form url.Values) string {
	var b strings.Builder
	b.WriteString(rawURL)
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		for _, v := range form[k] {
			b.WriteString(k)
			b.WriteString(v)
		}
	}
	m := hmac.New(sha1.New, []byte(token))
	m.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// Segments returns the billed segment count and encoding.
func Segments(text string) (int, string) {
	if septets, ok := gsmSeptets(text); ok {
		if septets <= 160 {
			return 1, SMSEncodingGSM7
		}
		return (septets + 152) / 153, SMSEncodingGSM7
	}
	units := len(utf16.Encode([]rune(text)))
	if units <= 70 {
		return 1, SMSEncodingUCS2
	}
	return (units + 66) / 67, SMSEncodingUCS2
}

// FitSMS caps text at maxSegments, appending suffix to a truncated body.
func FitSMS(text string, maxSegments int, suffix string) (body string, segments int, encoding string, truncated bool) {
	n, enc := Segments(text)
	if n <= maxSegments {
		return text, n, enc, false
	}
	runes := []rune(text)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if m, _ := Segments(string(runes[:mid]) + suffix); m <= maxSegments {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	body = strings.TrimRight(string(runes[:lo]), " \n\t") + suffix
	n, enc = Segments(body)
	return body, n, enc, true
}

func gsmSeptets(text string) (int, bool) {
	total := 0
	for _, r := range text {
		switch {
		case strings.ContainsRune(gsmBasic, r):
			total++
		case strings.ContainsRune(gsmExtension, r):
			total += 2
		default:
			return 0, false
		}
	}
	return total, true
}

const (
	gsmBasic     = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	gsmExtension = "^{}\\[~]|€"
)
