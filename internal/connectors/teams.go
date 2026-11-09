package connectors

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// teamsSubscriptionLifetime is the Graph subscription renewal window used by
// the pipeline. It stays below the 60 minute limit of transcript resources.
const teamsSubscriptionLifetime = 55 * time.Minute

// teamsHandler serves the Teams meeting pipeline over the Graph subscription
// API. It shares the Graph application credentials but has its own egress
// lane and token cache.
type teamsHandler struct {
	doer    *Doer
	tokDoer *Doer
	cfg     GraphSection
	sec     SecretSource
	tokens  *tokenCache
	now     func() time.Time
	base    string
	notify  string
}

// TeamsSection is [modules.connectors.teams].
type TeamsSection struct {
	Enabled         bool   `json:"enabled"`
	NotificationURL string `json:"notification_url"`
}

func (m *Module) newTeams() (Handler, error) {
	tcfg, err := decodeSub[TeamsSection](m.cfg.Teams, "teams")
	if err != nil || !tcfg.Enabled {
		return nil, err
	}
	gcfg, err := m.graphConfig()
	if err != nil {
		return nil, err
	}
	if !gcfg.Enabled {
		return nil, &Error{Code: codeInvalidArgs, Message: "teams requires graph credentials"}
	}
	doer, err := m.newDoer(m.network.Client("teams", netx.Tool))
	if err != nil {
		return nil, err
	}
	tokDoer, err := m.newDoer(m.network.Client("auth", netx.Inference))
	if err != nil {
		return nil, err
	}
	t := &teamsHandler{
		doer: doer, tokDoer: tokDoer, cfg: gcfg, sec: m.deps.Secrets, now: m.deps.Now,
		base: graphBase(&gcfg), notify: tcfg.NotificationURL,
	}
	g := &graphHandler{doer: doer, tokDoer: tokDoer, cfg: gcfg, sec: m.deps.Secrets, now: m.deps.Now}
	t.tokens = newTokenCache(g.refresh, m.deps.Now, m.redactor)
	return t, nil
}

func (t *teamsHandler) Tools() []string { return []string{"teams_pipeline"} }

type teamsArgs struct {
	Action         string `json:"action"`
	SubscriptionID string `json:"subscription_id"`
	MeetingID      string `json:"meeting_id"`
}

func (t *teamsHandler) Invoke(ctx context.Context, tool string, raw json.RawMessage, _ host.Env) (host.Result, error) {
	if tool != "teams_pipeline" {
		return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "unknown teams tool " + tool}
	}
	var a teamsArgs
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	tok, err := t.tokens.Token(ctx)
	if err != nil {
		return host.Result{}, err
	}
	switch a.Action {
	case "subscribe":
		return t.subscribe(ctx, tok)
	case "renew":
		return t.renew(ctx, tok, a.SubscriptionID)
	case "status":
		return t.status(ctx, tok, a.SubscriptionID)
	case "transcripts":
		return t.transcripts(ctx, tok, a.MeetingID)
	}
	return host.Result{}, &Error{Code: codeInvalidArgs, Message: "action must be subscribe, renew, status or transcripts"}
}

func (t *teamsHandler) subscribe(ctx context.Context, tok string) (host.Result, error) {
	if t.notify == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "teams.notification_url is required for subscribe"}
	}
	body, err := json.Marshal(map[string]string{
		"changeType":         "created",
		"notificationUrl":    t.notify,
		"resource":           "communications/onlineMeetings/getAllTranscripts",
		"expirationDateTime": t.now().UTC().Add(teamsSubscriptionLifetime).Format(time.RFC3339),
	})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode subscription", err: err}
	}
	return t.call(ctx, http.MethodPost, "/subscriptions", tok, body)
}

func (t *teamsHandler) renew(ctx context.Context, tok, id string) (host.Result, error) {
	segment, err := escapePath(id)
	if err != nil {
		return host.Result{}, err
	}
	body, err := json.Marshal(map[string]string{
		"expirationDateTime": t.now().UTC().Add(teamsSubscriptionLifetime).Format(time.RFC3339),
	})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode renewal", err: err}
	}
	return t.call(ctx, http.MethodPatch, "/subscriptions/"+segment, tok, body)
}

func (t *teamsHandler) status(ctx context.Context, tok, id string) (host.Result, error) {
	segment, err := escapePath(id)
	if err != nil {
		return host.Result{}, err
	}
	return t.call(ctx, http.MethodGet, "/subscriptions/"+segment, tok, nil)
}

// transcripts lists the meeting transcripts with a bounded $top.
func (t *teamsHandler) transcripts(ctx context.Context, tok, meetingID string) (host.Result, error) {
	if meetingID == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "meeting_id is required"}
	}
	segments := make([]string, 0, 2)
	for part := range strings.SplitSeq(strings.Trim(meetingID, "/"), "/") {
		segment, err := escapePath(part)
		if err != nil {
			return host.Result{}, err
		}
		segments = append(segments, segment)
	}
	query := url.Values{"$top": {"25"}}
	path := "/me/onlineMeetings/" + strings.Join(segments, "/") + "/transcripts?" + query.Encode()
	return t.call(ctx, http.MethodGet, path, tok, nil)
}

// call runs one Graph pipeline request. Only reads retry; subscribe and renew
// are mutations and are sent once.
func (t *teamsHandler) call(ctx context.Context, method, path, tok string, body []byte) (host.Result, error) {
	var out json.RawMessage
	req := Request{
		Method: method, URL: joinURL(t.base, path), Header: bearerHeader(tok),
		Accept: "application/json", Retry: method == http.MethodGet, MaxBody: DefaultMaxBytes,
	}
	if body != nil {
		req.Body = body
		maps.Copy(req.Header, headerJSON())
	}
	if _, err := t.doer.JSON(ctx, &req, &out); err != nil {
		return host.Result{}, err
	}
	if len(out) == 0 {
		out = json.RawMessage(`{}`)
	}
	return host.Result{Content: string(out), UntrustedSource: "connector:teams"}, nil
}
