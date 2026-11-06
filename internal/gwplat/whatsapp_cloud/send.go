// Outbound half of the Cloud API adapter (PLAT-026, GAP-GW-01): the 24-hour
// customer service window, templates, media ceilings and the Graph calls. The
// adapter never sleeps inline; pacing belongs to the shared outq scheduler.
package whatsapp_cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// Contract limits of the outbound Cloud API surface.
const (
	// GraphBaseURL is the production Graph API root.
	GraphBaseURL = "https://graph.facebook.com"
	// DefaultAPIVersion is the pinned Graph API version.
	DefaultAPIVersion = "v21.0"
	// SessionWindow is the 24-hour customer service window.
	SessionWindow = 24 * time.Hour
)

// WindowClosed is the typed 24-hour rejection; its Reason is the wire code.
type WindowClosed struct {
	Chat        string
	LastInbound time.Time
	At          time.Time
}

// Error implements error.
func (e *WindowClosed) Error() string {
	return fmt.Sprintf("%s (chat %s, last inbound %s)", ErrWindowClosed, e.Chat, e.LastInbound.UTC().Format(time.RFC3339))
}

// Unwrap makes errors.Is(err, ErrWindowClosed) true.
func (e *WindowClosed) Unwrap() error { return ErrWindowClosed }

// Reason is the stable rejection code reported to the caller.
func (e *WindowClosed) Reason() string { return "session_window_closed" }

func defaultClient(base string) *http.Client {
	parsed, err := url.Parse(base)
	if err == nil && isLoopback(parsed) {
		return netx.LoopbackClient()
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func isLoopback(u *url.URL) bool {
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (a *Adapter) baseURL() string {
	if a.cfg.BaseURL != "" {
		return strings.TrimRight(a.cfg.BaseURL, "/")
	}
	return GraphBaseURL
}

func (a *Adapter) apiVersion() string {
	if a.cfg.APIVersion != "" {
		return a.cfg.APIVersion
	}
	return DefaultAPIVersion
}

// Reply renders one markdown reply through outq and sends it as session
// messages, so the 24-hour guard applies to every chunk.
func (a *Adapter) Reply(ctx context.Context, r gwplat.Reply) error {
	r.Send = func(ctx context.Context, text string) error {
		return a.SendSession(ctx, r.Chat, text)
	}
	if err := a.out.Reply(ctx, r); err != nil {
		return fmt.Errorf("whatsapp_cloud: outbox reply: %w", err)
	}
	return nil
}

// SendSession sends free-form text inside the 24-hour window. Outside it the
// call fails typed before any request is written.
func (a *Adapter) SendSession(ctx context.Context, chat, text string) error {
	last, ok := a.lastInboundAt(chat)
	if !ok || !a.now().Before(last.Add(SessionWindow)) {
		return &WindowClosed{Chat: chat, LastInbound: last, At: a.now()}
	}
	msg := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                chat,
		"type":              "text",
		"text":              map[string]any{"preview_url": false, "body": text},
	}
	return a.post(ctx, msg)
}

func (a *Adapter) lastInboundAt(chat string) (time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	last, ok := a.lastInbound[chat]
	return last, ok
}

// SendTemplate sends a pre-approved template; the only message legal after the
// window closes.
func (a *Adapter) SendTemplate(ctx context.Context, chat, name, language string, params []string) error {
	if name == "" || language == "" {
		return fmt.Errorf("%w: template name and language", ErrConfig)
	}
	template := map[string]any{"name": name, "language": map[string]any{"code": language}}
	if len(params) > 0 {
		values := make([]map[string]any, len(params))
		for i, p := range params {
			values[i] = map[string]any{"type": "text", "text": p}
		}
		template["components"] = []map[string]any{{"type": "body", "parameters": values}}
	}
	return a.post(ctx, map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                chat,
		"type":              "template",
		"template":          template,
	})
}

// Media is one outbound attachment.
type Media struct {
	Chat    string
	Kind    string // image, audio, video, document, sticker
	ID      string // uploaded media id
	Link    string // public https link, alternative to ID
	Caption string
	Size    int64 // declared size; checked before the request
	Mime    string
}

// SendMedia sends one attachment. An oversize attachment fails before any
// request; the Cloud API upload path itself is owned by the caller.
func (a *Adapter) SendMedia(ctx context.Context, m *Media) error {
	limit, known := mediaLimit(m.Kind)
	if !known || m.Size < 0 || m.Size > limit || m.Size > MaxMediaBytes {
		return fmt.Errorf("%w: %s", ErrMediaTooLarge, m.Kind)
	}
	if m.ID == "" && m.Link == "" {
		return fmt.Errorf("%w: media id or link", ErrConfig)
	}
	part := map[string]any{"caption": m.Caption}
	if m.ID != "" {
		part["id"] = m.ID
	} else {
		part["link"] = m.Link
	}
	return a.post(ctx, map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                m.Chat,
		"type":              m.Kind,
		m.Kind:              part,
	})
}

// DownloadMedia fetches one media part with a hard byte ceiling. The Graph
// media endpoint returns a short-lived URL; both hops carry the bearer token.
func (a *Adapter) DownloadMedia(ctx context.Context, mediaID string) ([]byte, string, error) {
	metaURL := fmt.Sprintf("%s/%s/%s", a.baseURL(), a.apiVersion(), url.PathEscape(mediaID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("%w: request: %w", ErrGraph, err)
	}
	req.Header.Set("Authorization", "Bearer "+a.accessToken)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrGraph, err)
	}
	defer a.drain(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", a.graphError(resp)
	}
	var meta struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
	}
	if decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&meta); decodeErr != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrGraph, decodeErr)
	}
	if meta.FileSize > MaxMediaBytes {
		return nil, "", fmt.Errorf("%w: %d bytes", ErrMediaTooLarge, meta.FileSize)
	}
	if meta.URL == "" {
		return nil, "", fmt.Errorf("%w: media url", ErrGraph)
	}
	body, err := a.download(ctx, meta.URL)
	if err != nil {
		return nil, "", err
	}
	return body, meta.MimeType, nil
}

func (a *Adapter) download(ctx context.Context, link string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: request: %w", ErrGraph, err)
	}
	req.Header.Set("Authorization", "Bearer "+a.accessToken)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGraph, err)
	}
	defer a.drain(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, a.graphError(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxMediaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrGraph, err)
	}
	if int64(len(body)) > MaxMediaBytes {
		return nil, fmt.Errorf("%w: body", ErrMediaTooLarge)
	}
	return body, nil
}

func (a *Adapter) post(ctx context.Context, msg map[string]any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: encode: %w", ErrGraph, err)
	}
	endpoint := fmt.Sprintf("%s/%s/%s/messages", a.baseURL(), a.apiVersion(), url.PathEscape(a.cfg.PhoneNumberID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("%w: request: %w", ErrGraph, err)
	}
	req.Header.Set("Authorization", "Bearer "+a.accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrGraph, err)
	}
	defer a.drain(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return a.graphError(resp)
	}
	return nil
}

// graphEnvelope is the Cloud API error shape; the 131047 code means the
// customer service window closed between the guard and the send.
type graphEnvelope struct {
	Error struct {
		Message      string `json:"message"`
		Type         string `json:"type"`
		Code         int    `json:"code"`
		ErrorSubcode int    `json:"error_subcode"`
	} `json:"error"`
}

func (a *Adapter) graphError(resp *http.Response) error {
	var env graphEnvelope
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if readErr == nil {
		if unmarshalErr := json.Unmarshal(body, &env); unmarshalErr != nil {
			env = graphEnvelope{}
		}
	}
	if env.Error.Code == 131047 {
		return &WindowClosed{Chat: "", LastInbound: time.Time{}, At: a.now()}
	}
	msg := env.Error.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%w: %d %s (code %d)", ErrGraph, resp.StatusCode, redactSecrets(msg, a.accessToken, a.appSecret, a.verifyToken), env.Error.Code)
}

// redactSecrets removes the adapter's own secrets from an upstream message;
// the Graph API never echoes token material today, and the guard keeps it so.
func redactSecrets(msg string, secrets ...string) string {
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	return msg
}

// drain discards the remaining body so the connection can be reused. A
// failure is only diagnostic: the response was already consumed or rejected.
func (a *Adapter) drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)); err != nil {
		a.log.Debug("whatsapp_cloud: drain body", "err", err)
	}
	if err := resp.Body.Close(); err != nil {
		a.log.Debug("whatsapp_cloud: close body", "err", err)
	}
}

func validateBase(cfg *Config) error {
	base := cfg.BaseURL
	if base == "" {
		base = GraphBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%w: base URL", ErrConfig)
	}
	if parsed.Scheme != "https" && !isLoopback(parsed) {
		return fmt.Errorf("%w: base URL must be https or loopback", ErrConfig)
	}
	return nil
}
