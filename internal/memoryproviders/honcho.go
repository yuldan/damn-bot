package memoryproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Honcho API bounds (v3): a message is at most 25.000 characters and a batch
// at most 100 messages. The framework's 1 MiB item cap still applies first.
const (
	maxHonchoMessageRunes = 25000
	maxHonchoBatch        = 100
	honchoDefaultBase     = "https://api.honcho.dev"
)

type honcho struct {
	base      string
	token     string
	workspace string
	session   string
	peer      string
	doer      Doer
	rewriter  Rewriter
	retries   int
	sleep     func(context.Context, time.Duration) error
}

// newHoncho validates the endpoint and credential. It performs no I/O (P16).
func newHoncho(s *Settings) (Provider, error) {
	if s == nil || s.HTTP == nil {
		return nil, fmt.Errorf("%w: honcho requires an HTTP seam", ErrInvalid)
	}
	base, err := honchoBase(s.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(s.Token) == "" {
		return nil, fmt.Errorf("%w: honcho token is not configured", ErrUnavailable)
	}
	if s.Retries < 0 || s.Retries > MaxRetries {
		return nil, fmt.Errorf("%w: retries must be 0..%d", ErrInvalid, MaxRetries)
	}
	sleep := s.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	return &honcho{
		base: base, token: s.Token,
		workspace: cmpOr(s.Workspace, "default"),
		session:   cmpOr(s.SessionID, "default"),
		peer:      cmpOr(s.PeerID, "damnbot"),
		doer:      s.HTTP, rewriter: s.Rewriter, retries: s.Retries, sleep: sleep,
	}, nil
}

func cmpOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// honchoBase accepts HTTPS, or plain HTTP only for a loopback fake server.
func honchoBase(raw string) (string, error) {
	raw = cmpOr(raw, honchoDefaultBase)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%w: honcho base_url", ErrInvalid)
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return "", fmt.Errorf("%w: honcho base_url must be https or loopback http", ErrInvalid)
	}
	return strings.TrimRight(raw, "/"), nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Name reports the provider name.
func (h *honcho) Name() string { return SourceHoncho }

type honchoSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type honchoMessage struct {
	ID        string         `json:"id"`
	Content   string         `json:"content"`
	PeerID    string         `json:"peer_id"`
	SessionID string         `json:"session_id"`
	Metadata  map[string]any `json:"metadata"`
}

// Recall searches the workspace and returns labelled snippets. The query is
// rewritten first when MEM-009 is enabled.
func (h *honcho) Recall(ctx context.Context, q Query) ([]Snippet, error) {
	text, err := h.rewrite(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit < 1 || limit > MaxQueryLimit {
		limit = DefaultQueryLimit
	}
	path := "/v3/workspaces/" + url.PathEscape(h.workspace) + "/search"
	var found []honchoMessage
	if err := h.doJSON(ctx, http.MethodPost, path, honchoSearchRequest{Query: text, Limit: limit}, &found); err != nil {
		return nil, err
	}
	out := make([]Snippet, 0, len(found))
	for i := range found {
		content := strings.TrimSpace(found[i].Content)
		if content == "" {
			continue
		}
		source := SourceHoncho
		if found[i].PeerID != "" {
			source += ":" + found[i].PeerID
		}
		out = append(out, Snippet{Source: source, Text: content})
	}
	return out, nil
}

func (h *honcho) rewrite(ctx context.Context, query string) (string, error) {
	if h.rewriter == nil {
		return query, nil
	}
	rewritten, err := h.rewriter.Rewrite(ctx, query)
	if err != nil {
		return "", fmt.Errorf("honcho: query rewrite: %w", err)
	}
	if strings.TrimSpace(rewritten) == "" {
		return query, nil
	}
	return strings.TrimSpace(rewritten), nil
}

type honchoMessageCreate struct {
	Content  string            `json:"content"`
	PeerID   string            `json:"peer_id"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type honchoBatch struct {
	Messages []honchoMessageCreate `json:"messages"`
}

// Store uploads one item, split at the per-message rune limit. The framework
// has already enforced the 1 MiB cap and the operation deadline.
func (h *honcho) Store(ctx context.Context, item Item) error {
	chunks := chunkRunes(item.Text, maxHonchoMessageRunes)
	if len(chunks) == 0 || len(chunks) > maxHonchoBatch {
		return fmt.Errorf("%w: item needs %d Honcho messages", ErrTooLarge, len(chunks))
	}
	msgs := make([]honchoMessageCreate, 0, len(chunks))
	for i, chunk := range chunks {
		meta := map[string]string(nil)
		if len(item.Metadata) > 0 || len(chunks) > 1 {
			meta = make(map[string]string, len(item.Metadata)+2)
			maps.Copy(meta, item.Metadata)
			if len(chunks) > 1 {
				meta["part"] = fmt.Sprintf("%d/%d", i+1, len(chunks))
			}
			if item.ID != "" {
				meta["item_id"] = item.ID
			}
		}
		msgs = append(msgs, honchoMessageCreate{Content: chunk, PeerID: h.peer, Metadata: meta})
	}
	path := "/v3/workspaces/" + url.PathEscape(h.workspace) +
		"/sessions/" + url.PathEscape(h.session) + "/messages"
	return h.doJSON(ctx, http.MethodPost, path, honchoBatch{Messages: msgs}, nil)
}

func chunkRunes(s string, limit int) []string {
	if s == "" || limit <= 0 {
		return nil
	}
	runes := []rune(s)
	out := make([]string, 0, (len(runes)+limit-1)/limit)
	for len(runes) > 0 {
		n := min(limit, len(runes))
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

// marshalJSON is shared by the search and message paths.
func marshalJSON(v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %w", ErrInvalid, err)
	}
	return body, nil
}

// sleepContext is the default retry backoff.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("memoryproviders: retry backoff: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
