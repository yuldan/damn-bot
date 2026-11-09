package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// graphMaxItems and graphMaxResultBytes bound one Graph tool result: a page
// request can return at most 100 items per page and at most five pages, and
// the rendered list stops growing at 128 KiB.
const (
	graphMaxItems       = 200
	graphMaxResultBytes = 128 << 10
	maxGraphSelect      = 8
)

// graphHandler serves TOOL-level Microsoft Graph reads. The token cache is
// shared by mail/calendar/files of this handler.
type graphHandler struct {
	doer    *Doer
	tokDoer *Doer
	cfg     GraphSection
	sec     SecretSource
	tokens  *tokenCache
	red     *Redactor
	now     func() time.Time
	base    string
}

// GraphSection is [modules.connectors.graph].
type GraphSection struct {
	Enabled         bool   `json:"enabled"`
	TenantID        string `json:"tenant_id"`
	ClientID        string `json:"client_id"`
	ClientSecretEnv string `json:"client_secret_env"`
	RefreshTokenEnv string `json:"refresh_token_env"`
	TokenURL        string `json:"token_url"`
	BaseURL         string `json:"base_url"`
}

// graphConfig decodes and validates one graph section.
func (m *Module) graphConfig() (GraphSection, error) {
	g, err := decodeSub[GraphSection](m.cfg.Graph, "graph")
	if err != nil || !g.Enabled {
		return g, err
	}
	if g.TenantID == "" || g.ClientID == "" {
		return g, &Error{Code: codeInvalidArgs, Message: "graph needs tenant_id and client_id"}
	}
	if g.ClientSecretEnv == "" && g.RefreshTokenEnv == "" {
		return g, &Error{Code: codeInvalidArgs, Message: "graph needs client_secret_env or refresh_token_env"}
	}
	return g, nil
}

func (m *Module) newGraph() (Handler, error) {
	cfg, err := m.graphConfig()
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	doer, err := m.newDoer(m.network.Client("graph", netx.Tool))
	if err != nil {
		return nil, err
	}
	tokDoer, err := m.newDoer(m.network.Client("auth", netx.Inference))
	if err != nil {
		return nil, err
	}
	g := &graphHandler{
		doer: doer, tokDoer: tokDoer, cfg: cfg, sec: m.deps.Secrets,
		red: m.redactor, now: m.deps.Now, base: graphBase(&cfg),
	}
	g.tokens = newTokenCache(g.refresh, m.deps.Now, m.redactor)
	return g, nil
}

func (g *graphHandler) Tools() []string {
	return []string{"ms_graph_mail", "ms_graph_calendar", "ms_graph_files"}
}

func (g *graphHandler) Invoke(ctx context.Context, tool string, args json.RawMessage, _ host.Env) (host.Result, error) {
	token, err := g.tokens.Token(ctx)
	if err != nil {
		return host.Result{}, err
	}
	switch tool {
	case "ms_graph_mail":
		return g.mail(ctx, token, args)
	case "ms_graph_calendar":
		return g.calendar(ctx, token, args)
	case "ms_graph_files":
		return g.files(ctx, token, args)
	}
	return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "unknown graph tool " + tool}
}

// refresh exchanges the configured grant for one Graph access token. The
// request is a form POST; the response is never echoed, and the token is
// registered with the redactor by tokenCache.
func (g *graphHandler) refresh(ctx context.Context) (token, error) {
	form := url.Values{"client_id": {g.cfg.ClientID}, "scope": {"https://graph.microsoft.com/.default offline_access"}}
	grant := "client_credentials"
	if g.cfg.RefreshTokenEnv != "" {
		grant = "refresh_token"
		refresh, err := ResolveSecret(g.sec, g.cfg.RefreshTokenEnv)
		if err != nil {
			return token{}, err
		}
		g.red.Add(refresh)
		form.Set("refresh_token", refresh)
	}
	form.Set("grant_type", grant)
	if g.cfg.ClientSecretEnv != "" {
		secret, err := ResolveSecret(g.sec, g.cfg.ClientSecretEnv)
		if err != nil {
			return token{}, err
		}
		g.red.Add(secret)
		form.Set("client_secret", secret)
	}
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	var out oauthToken
	if _, err := g.tokDoer.JSON(ctx, &Request{
		Method: http.MethodPost, URL: graphTokenURL(&g.cfg), Header: header,
		Body: []byte(form.Encode()), Accept: "application/json",
	}, &out); err != nil {
		return token{}, err
	}
	if out.AccessToken == "" {
		return token{}, &Error{Code: codeAuthRequired, Message: "token endpoint returned no access_token"}
	}
	return token{Value: out.AccessToken, Expiry: expiryOf(g.now(), out.ExpiresIn)}, nil
}

// graphBase returns the configured Graph base or its public default.
func graphBase(g *GraphSection) string {
	if g.BaseURL != "" {
		return strings.TrimRight(g.BaseURL, "/")
	}
	return "https://graph.microsoft.com/v1.0"
}

// graphTokenURL returns the configured token endpoint or the public default.
func graphTokenURL(g *GraphSection) string {
	if g.TokenURL != "" {
		return g.TokenURL
	}
	return "https://login.microsoftonline.com/" + url.PathEscape(g.TenantID) + "/oauth2/v2.0/token"
}

// mailArgs is one ms_graph_mail call.
type mailArgs struct {
	Folder string   `json:"folder"`
	Top    int      `json:"top"`
	Page   int      `json:"page"`
	Select []string `json:"select"`
}

func (g *graphHandler) mail(ctx context.Context, tok string, raw json.RawMessage) (host.Result, error) {
	var a mailArgs
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	path := "/me/messages"
	if a.Folder != "" {
		segment, err := escapePath(a.Folder)
		if err != nil {
			return host.Result{}, err
		}
		path = "/me/mailFolders/" + segment + "/messages"
	}
	return g.list(ctx, tok, path, a.Top, a.Page, a.Select, nil)
}

// calendarArgs is one ms_graph_calendar call.
type calendarArgs struct {
	Days   int      `json:"days"`
	Top    int      `json:"top"`
	Page   int      `json:"page"`
	Select []string `json:"select"`
}

func (g *graphHandler) calendar(ctx context.Context, tok string, raw json.RawMessage) (host.Result, error) {
	var a calendarArgs
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	if a.Days < 0 || a.Days > 90 {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "days must be 1..90"}
	}
	if a.Days == 0 {
		a.Days = 7
	}
	start := g.now().UTC()
	extra := url.Values{
		"startDateTime": {start.Format(time.RFC3339)},
		"endDateTime":   {start.AddDate(0, 0, a.Days).Format(time.RFC3339)},
	}
	return g.list(ctx, tok, "/me/calendarView", a.Top, a.Page, a.Select, extra)
}

// filesArgs is one ms_graph_files call.
type filesArgs struct {
	Path string `json:"path"`
	Top  int    `json:"top"`
	Page int    `json:"page"`
}

func (g *graphHandler) files(ctx context.Context, tok string, raw json.RawMessage) (host.Result, error) {
	var a filesArgs
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	path := "/me/drive/root/children"
	if a.Path != "" {
		escaped := make([]string, 0, 4)
		for part := range strings.SplitSeq(strings.Trim(a.Path, "/"), "/") {
			segment, err := escapePath(part)
			if err != nil {
				return host.Result{}, err
			}
			escaped = append(escaped, segment)
		}
		path = "/me/drive/root:/" + strings.Join(escaped, "/") + ":/children"
	}
	return g.list(ctx, tok, path, a.Top, a.Page, nil, nil)
}

// list executes the bounded page loop: at most five pages, 100 items each,
// and at most 128 KiB of rendered items. A page beyond the cap is an
// invalid argument rather than an unbounded request.
func (g *graphHandler) list(ctx context.Context, tok, path string, top, page int, selectFields []string, extra url.Values) (host.Result, error) {
	top, pages, err := graphPaging(top, page)
	if err != nil {
		return host.Result{}, err
	}
	if len(selectFields) > maxGraphSelect {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "select has at most 8 fields"}
	}
	items := make([]json.RawMessage, 0, top)
	size := 0
	truncated := false
	for p := range pages {
		itemsPage, perr := g.fetchPage(ctx, tok, path, graphQuery(top, p*top, selectFields, extra))
		if perr != nil {
			return host.Result{}, perr
		}
		items, truncated = collectGraphItems(items, itemsPage, &size)
		if truncated || len(itemsPage) < top {
			break
		}
	}
	out, err := json.Marshal(struct {
		Items     []json.RawMessage `json:"items"`
		Truncated bool              `json:"truncated"`
	}{items, truncated})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode result", err: err}
	}
	return host.Result{Content: string(out), UntrustedSource: "connector:graph"}, nil
}

// graphQuery builds the bounded paging query of one Graph page.
func graphQuery(top, skip int, selectFields []string, extra url.Values) url.Values {
	query := url.Values{"$top": {strconv.Itoa(top)}, "$skip": {strconv.Itoa(skip)}}
	if len(selectFields) > 0 {
		query.Set("$select", strings.Join(selectFields, ","))
	}
	for k, vs := range extra {
		query[k] = append(query[k], vs...)
	}
	return query
}

// collectGraphItems appends one page until the item or byte cap is reached.
func collectGraphItems(items, page []json.RawMessage, size *int) ([]json.RawMessage, bool) {
	for _, item := range page {
		items = append(items, item)
		*size += len(item)
		if len(items) >= graphMaxItems || *size >= graphMaxResultBytes {
			return items, true
		}
	}
	return items, false
}

// fetchPage reads one Graph page. The response is bounded by the Doer and
// decoded into raw items so no field of an item is re-encoded.
func (g *graphHandler) fetchPage(ctx context.Context, tok, path string, query url.Values) ([]json.RawMessage, error) {
	var body struct {
		Value []json.RawMessage `json:"value"`
	}
	if _, err := g.doer.JSON(ctx, &Request{
		Method: http.MethodGet, URL: joinURL(g.base, path) + "?" + query.Encode(),
		Header: bearerHeader(tok), Accept: "application/json",
		Retry: true, MaxBody: DefaultMaxBytes,
	}, &body); err != nil {
		return nil, err
	}
	return body.Value, nil
}

// graphPaging clamps one call's paging request.
func graphPaging(top, page int) (int, int, error) {
	if top < 0 || top > 100 {
		return 0, 0, &Error{Code: codeInvalidArgs, Message: "top must be 1..100"}
	}
	if top == 0 {
		top = 25
	}
	if page < 0 || page > maxPagesDefault {
		return 0, 0, &Error{Code: codeInvalidArgs, Message: "page must be 1..5"}
	}
	if page == 0 {
		page = 1
	}
	return top, page, nil
}

// newDoer builds the shared Doer of one connector over its own egress lane.
func (m *Module) newDoer(client *netx.Client) (*Doer, error) {
	return NewDoer(DoerOptions{
		Client: client, Timeout: m.cfg.Timeout, Retries: m.cfg.Retries,
		MaxBytes: m.cfg.MaxBytes, Redactor: m.redactor,
		Now: m.deps.Now, Sleep: m.deps.Sleep,
	})
}
