package webhook

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// Microsoft Graph contract limits.
const (
	// GraphBaseURL is the production Graph API root.
	GraphBaseURL = "https://graph.microsoft.com"
	// GraphAPIVersion is the pinned Graph API version.
	GraphAPIVersion = "v1.0"
	// DefaultLifetime is the renewal period of a message subscription.
	DefaultLifetime = 55 * time.Minute
	// DefaultRenewLead renews a subscription before Graph expires it.
	DefaultRenewLead = 15 * time.Minute
	// MaxValidationToken bounds the plain echo of the handshake.
	MaxValidationToken = 4096
	// MaxResourceBytes bounds one resource path in a notification.
	MaxResourceBytes = 2048
	// MaxResponseBytes bounds a Graph API response body.
	MaxResponseBytes = 16 << 10
)

var validationToken = regexp.MustCompile(`^[A-Za-z0-9 ._~:/+=-]{1,1000}$`)

// HTTPDoer is the HTTP seam of the Graph surface. *netx.Client implements it;
// tests inject an httptest client, so E-05 stays closed (no literal
// http.Client outside internal/netx).
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// closeBody closes a response body; callers drain it first.
func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		return
	}
}

// MS Graph errors. Compared with errors.Is.
var (
	ErrGraph               = errors.New("msgraph_webhook: graph API error")
	ErrClientState         = errors.New("msgraph_webhook: clientState mismatch")
	ErrUnknownSubscription = errors.New("msgraph_webhook: unknown subscription")
	ErrValidation          = errors.New("msgraph_webhook: invalid validationToken")
)

// TokenSource returns a bearer token for the Graph subscription API. It is a
// config seam: this package never reads the environment and never logs the
// token.
type TokenSource func(ctx context.Context) (string, error)

// MSGraphConfig is the explicit activation of the Graph notification surface.
type MSGraphConfig struct {
	Enabled      bool
	BaseURL      string
	Doer         HTTPDoer
	Token        TokenSource
	Resource     string
	ChangeType   string
	Lifetime     time.Duration
	RenewLead    time.Duration
	MaxBodyBytes int64
	Log          *slog.Logger
	Now          func() time.Time
}

// GraphAdapter receives Graph change notifications and owns the subscription
// lifecycle (create, renew before expiry, delete).
type GraphAdapter struct {
	cfg         MSGraphConfig
	desc        *gwplat.Descriptor
	store       *Store
	h           gwplat.Handler
	log         *slog.Logger
	now         func() time.Time
	clientState []byte
	dedupe      *gwplat.Dedupe
}

// NewGraph activates the Graph surface. The clientState secret is required:
// without it the endpoint stays inactive rather than accepting foreign
// notifications.
func NewGraph(ctx context.Context, cfg *MSGraphConfig, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	store *Store, h gwplat.Handler,
) (*GraphAdapter, error) {
	if desc == nil || desc.Name != "msgraph_webhook" {
		return nil, fmt.Errorf("%w: descriptor is not msgraph_webhook", ErrConfig)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("msgraph_webhook: activation: %w", err)
	}
	if store == nil || h == nil {
		return nil, fmt.Errorf("%w: subscription store and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("msgraph_webhook: activation: %w", err)
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	a := &GraphAdapter{
		cfg:         *cfg,
		desc:        desc,
		store:       store,
		h:           h,
		log:         logger,
		now:         now,
		clientState: []byte(values["msgraph_client_state"]),
		dedupe:      gwplat.NewDedupe(DefaultDedupeSize, DefaultDedupeTTL),
	}
	a.applyDefaults()
	return a, nil
}

func (a *GraphAdapter) applyDefaults() {
	a.cfg.Lifetime = cmp.Or(a.cfg.Lifetime, DefaultLifetime)
	a.cfg.RenewLead = cmp.Or(a.cfg.RenewLead, DefaultRenewLead)
	a.cfg.MaxBodyBytes = cmp.Or(a.cfg.MaxBodyBytes, int64(MaxBodyBytes))
	a.cfg.Resource = cmp.Or(a.cfg.Resource, "/me/messages")
	a.cfg.ChangeType = cmp.Or(a.cfg.ChangeType, "created")
}

// HandleValidation echoes the handshake token verbatim. Anything else (empty,
// oversized, control characters) is refused without an echo.
func (a *GraphAdapter) HandleValidation(token string) (string, error) {
	if len(token) > MaxValidationToken || !validationToken.MatchString(token) {
		return "", ErrValidation
	}
	return token, nil
}

type graphNotification struct {
	SubscriptionID string `json:"subscriptionId"`
	ClientState    string `json:"clientState"`
	ChangeType     string `json:"changeType"`
	Resource       string `json:"resource"`
	TenantID       string `json:"tenantId"`
}

type graphBatch struct {
	Value []graphNotification `json:"value"`
}

// HandleNotification validates every entry before dispatching any: one bad
// clientState, unknown subscription or foreign changeType rejects the batch
// and runs 0 turns.
func (a *GraphAdapter) HandleNotification(ctx context.Context, body []byte) (int, error) {
	if int64(len(body)) > a.cfg.MaxBodyBytes {
		return 0, ErrTooLarge
	}
	var batch graphBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	if len(batch.Value) == 0 {
		return 0, fmt.Errorf("%w: empty notification batch", ErrInvalidPayload)
	}
	for i := range batch.Value {
		if err := a.check(&batch.Value[i]); err != nil {
			return 0, err
		}
	}
	dispatched := 0
	for i := range batch.Value {
		n, err := a.dispatch(ctx, &batch.Value[i])
		dispatched += n
		if err != nil {
			return dispatched, err
		}
	}
	return dispatched, nil
}

func (a *GraphAdapter) check(n *graphNotification) error {
	if !hmac.Equal([]byte(n.ClientState), a.clientState) {
		return ErrClientState
	}
	if n.SubscriptionID == "" {
		return fmt.Errorf("%w: subscriptionId", ErrInvalidPayload)
	}
	if _, ok := a.store.Get(n.SubscriptionID); !ok {
		return fmt.Errorf("%w: %s", ErrUnknownSubscription, n.SubscriptionID)
	}
	switch n.ChangeType {
	case "created", "updated", "deleted":
	default:
		return fmt.Errorf("%w: changeType %q", ErrInvalidPayload, n.ChangeType)
	}
	if len(n.Resource) == 0 || len(n.Resource) > MaxResourceBytes || !strings.HasPrefix(n.Resource, "/") ||
		strings.ContainsAny(n.Resource, "\x00\r\n") {
		return fmt.Errorf("%w: resource", ErrInvalidPayload)
	}
	return nil
}

func (a *GraphAdapter) dispatch(ctx context.Context, n *graphNotification) (int, error) {
	key := "msgraph:" + n.SubscriptionID + "|" + n.ChangeType + "|" + n.Resource
	if !a.dedupe.Claim(key, a.now()) {
		return 0, nil
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	in := &gateway.Inbound{
		Platform:  a.desc.Name,
		ChatID:    chatFromResource(n.Resource),
		SenderID:  "msgraph",
		UpdateID:  key,
		Payload:   string(raw),
		Group:     true,
		Mentioned: true,
	}
	if err := gwplat.Dispatch(ctx, a.h, in); err != nil {
		return 0, fmt.Errorf("msgraph_webhook: dispatch: %w", err)
	}
	return 1, nil
}

// chatFromResource maps a Graph resource to the Teams chat id the pipeline
// routes on; without one the resource itself is the chat key.
func chatFromResource(resource string) string {
	const prefix = "chats('"
	if _, rest, ok := strings.Cut(resource, prefix); ok {
		if end := strings.Index(rest, "')"); end > 0 {
			if id := rest[:end]; ValidID(id) {
				return id
			}
		}
	}
	if len(resource) > MaxIDBytes {
		return resource[:MaxIDBytes]
	}
	return resource
}

// ServeHTTP serves both Graph endpoints: POST ?validationToken= echoes the
// token as text/plain, any other POST is a change notification.
func (a *GraphAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if token, ok := r.URL.Query()["validationToken"]; ok {
		echo, err := a.HandleValidation(first(token))
		if err != nil {
			http.Error(w, "invalid validationToken", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		if _, werr := io.WriteString(w, echo); werr != nil { // #nosec G705 -- validated token, text/plain, nosniff
			return
		}
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, a.cfg.MaxBodyBytes+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	n, err := a.HandleNotification(r.Context(), body)
	if err != nil {
		a.rejectNotification(w, r, err)
		return
	}
	a.log.DebugContext(r.Context(), "msgraph notification", "dispatched", n)
	w.WriteHeader(http.StatusAccepted)
}

func (a *GraphAdapter) rejectNotification(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTooLarge):
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, ErrClientState), errors.Is(err, ErrUnknownSubscription):
		a.log.DebugContext(r.Context(), "msgraph notification rejected", "err", err)
		http.Error(w, "unknown notification", http.StatusBadRequest)
	case errors.Is(err, ErrInvalidPayload):
		http.Error(w, "invalid payload", http.StatusBadRequest)
	default:
		a.log.DebugContext(r.Context(), "msgraph notification failed", "err", err)
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

type graphSubscription struct {
	ChangeType         string `json:"changeType"`
	NotificationURL    string `json:"notificationUrl"`
	Resource           string `json:"resource"`
	ExpirationDateTime string `json:"expirationDateTime"`
	ClientState        string `json:"clientState"`
}

type graphCreated struct {
	ID                 string `json:"id"`
	ExpirationDateTime string `json:"expirationDateTime"`
}

// Subscribe creates the Graph subscription and records it. An identical
// stored target is returned as-is (idempotent subscribe).
func (a *GraphAdapter) Subscribe(ctx context.Context, sub *Subscription) (Subscription, bool, error) {
	if sub.Provider == "" {
		sub.Provider = "msgraph"
	}
	if sub.SecretRef == "" {
		sub.SecretRef = "msgraph_client_state"
	}
	if err := ValidateSubscription(sub); err != nil {
		return Subscription{}, false, err
	}
	if sub.Provider != "msgraph" {
		return Subscription{}, false, fmt.Errorf("%w: provider %q", ErrSubscription, sub.Provider)
	}
	existingRows := a.store.List()
	for i := range existingRows {
		existing := &existingRows[i]
		if existing.Provider == sub.Provider && existing.URL == sub.URL &&
			existing.SecretRef == sub.SecretRef {
			return *existing, false, nil
		}
	}
	expires := sub.ExpiresAt
	if expires.IsZero() {
		expires = a.now().Add(a.cfg.Lifetime)
	}
	var created graphCreated
	if err := a.do(ctx, http.MethodPost, "subscriptions", a.createPayload(sub.URL, expires), &created); err != nil {
		return Subscription{}, false, err
	}
	sub.ID, sub.ExpiresAt = created.ID, expires.UTC()
	stored, added, err := a.store.Add(sub)
	if err != nil {
		return Subscription{}, false, err
	}
	if !added {
		// A concurrent writer won the idempotent race; drop the duplicate row.
		if derr := a.do(ctx, http.MethodDelete, "subscriptions/"+created.ID, nil, nil); derr != nil {
			a.log.DebugContext(ctx, "msgraph duplicate cleanup failed", "err", derr)
		}
	}
	return stored, added, nil
}

func (a *GraphAdapter) createPayload(url string, expires time.Time) *graphSubscription {
	return &graphSubscription{
		ChangeType:         a.cfg.ChangeType,
		NotificationURL:    url,
		Resource:           a.cfg.Resource,
		ExpirationDateTime: expires.UTC().Format(time.RFC3339),
		ClientState:        string(a.clientState),
	}
}

// RenewDue renews every subscription inside the lead window. It returns the
// number renewed; failures are joined so one bad row cannot hide the rest.
func (a *GraphAdapter) RenewDue(ctx context.Context) (int, error) {
	now := a.now()
	due := a.store.Due(now, a.cfg.RenewLead)
	var errs []error
	renewed := 0
	for i := range due {
		if due[i].Provider != "msgraph" {
			continue
		}
		expires := now.Add(a.cfg.Lifetime).UTC()
		payload := map[string]string{"expirationDateTime": expires.Format(time.RFC3339)}
		if err := a.do(ctx, http.MethodPatch, "subscriptions/"+due[i].ID, payload, nil); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := a.store.SetExpiry(due[i].ID, expires); err != nil {
			errs = append(errs, err)
			continue
		}
		renewed++
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return renewed, errors.Join(errs...)
}

// RemoveSubscription deletes the Graph row (404 is already gone) and the
// stored subscription.
func (a *GraphAdapter) RemoveSubscription(ctx context.Context, id string) error {
	if err := a.do(ctx, http.MethodDelete, "subscriptions/"+id, nil, nil); err != nil &&
		!errors.Is(err, errGraphGone) {
		return err
	}
	if err := a.store.Remove(id); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

var errGraphGone = errors.New("msgraph_webhook: subscription already gone")

func (a *GraphAdapter) do(ctx context.Context, method, path string, payload, out any) error {
	doer := a.cfg.Doer
	if doer == nil {
		return fmt.Errorf("%w: no HTTP client", ErrGraph)
	}
	token, err := a.token(ctx)
	if err != nil {
		return err
	}
	body, err := encodePayload(payload)
	if err != nil {
		return err
	}
	base := a.cfg.BaseURL
	if base == "" {
		base = GraphBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(base, "/")+"/"+GraphAPIVersion+"/"+path, body)
	if err != nil {
		return fmt.Errorf("%w: request: %w", ErrGraph, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := doer.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrGraph, err)
	}
	defer closeBody(resp)
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: response: %w", ErrGraph, err)
	}
	return graphResponse(method, resp.StatusCode, data, out)
}

func (a *GraphAdapter) token(ctx context.Context) (string, error) {
	if a.cfg.Token == nil {
		return "", fmt.Errorf("%w: no token source", ErrGraph)
	}
	token, err := a.cfg.Token(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("%w: token unavailable", ErrGraph)
	}
	return token, nil
}

func encodePayload(payload any) (io.Reader, error) {
	if payload == nil {
		return nil, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %w", ErrGraph, err)
	}
	return bytes.NewReader(data), nil
}

func graphResponse(method string, status int, data []byte, out any) error {
	if status == http.StatusNotFound && method == http.MethodDelete {
		return errGraphGone
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("%w: status %d", ErrGraph, status)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrGraph, err)
	}
	return nil
}
