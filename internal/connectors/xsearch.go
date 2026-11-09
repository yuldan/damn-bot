package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// xSearchMaxContent bounds one search answer.
const xSearchMaxContent = 64 << 10

// xSearchHandler calls the xAI chat endpoint with X live search enabled. The
// API key comes from config by env name and never appears in a result.
type xSearchHandler struct {
	doer *Doer
	cfg  XSearchSection
	sec  SecretSource
	red  *Redactor
	base string
}

// XSearchSection is [modules.connectors.x_search].
type XSearchSection struct {
	Enabled bool   `json:"enabled"`
	KeyEnv  string `json:"key_env"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

func (m *Module) newXSearch() (Handler, error) {
	cfg, err := decodeSub[XSearchSection](m.cfg.XSearch, "x_search")
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	if cfg.KeyEnv == "" {
		return nil, &Error{Code: codeInvalidArgs, Message: "x_search.key_env is required"}
	}
	doer, err := m.newDoer(m.network.Client("xsearch", netx.Tool))
	if err != nil {
		return nil, err
	}
	return &xSearchHandler{
		doer: doer, cfg: cfg, sec: m.deps.Secrets,
		red: m.redactor, base: xSearchBase(cfg),
	}, nil
}

func xSearchBase(x XSearchSection) string {
	if x.BaseURL != "" {
		return strings.TrimRight(x.BaseURL, "/")
	}
	return "https://api.x.ai"
}

func (x *xSearchHandler) Tools() []string { return []string{"x_search"} }

func (x *xSearchHandler) Invoke(ctx context.Context, tool string, raw json.RawMessage, _ host.Env) (host.Result, error) {
	if tool != "x_search" {
		return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "unknown x_search tool " + tool}
	}
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	if strings.TrimSpace(a.Query) == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "query must not be empty"}
	}
	if a.Limit < 0 || a.Limit > 20 {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "limit must be 1..20"}
	}
	if a.Limit == 0 {
		a.Limit = 10
	}
	key, err := ResolveSecret(x.sec, x.cfg.KeyEnv)
	if err != nil {
		return host.Result{}, err
	}
	x.red.Add(key)
	model := x.cfg.Model
	if model == "" {
		model = "grok-4"
	}
	payload := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": a.Query}},
		"search_parameters": map[string]any{
			"mode": "on", "sources": []map[string]string{{"type": "x"}},
			"max_search_results": a.Limit,
		},
		"max_tokens": 2048,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode x_search", err: err}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Citations []string `json:"citations"`
	}
	if _, perr := x.doer.JSON(ctx, &Request{
		Method: http.MethodPost,
		URL:    joinURL(x.base, "/v1/chat/completions"), Header: bearerHeader(key),
		Body: body, Accept: "application/json", Retry: true,
	}, &out); perr != nil {
		return host.Result{}, perr
	}
	content := ""
	if len(out.Choices) > 0 {
		content = out.Choices[0].Message.Content
	}
	if len(content) > xSearchMaxContent {
		content = content[:xSearchMaxContent]
	}
	rendered, err := json.Marshal(struct {
		Content   string   `json:"content"`
		Citations []string `json:"citations,omitempty"`
	}{content, out.Citations})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode x_search result", err: err}
	}
	return host.Result{Content: string(rendered), UntrustedSource: "connector:xsearch"}, nil
}
