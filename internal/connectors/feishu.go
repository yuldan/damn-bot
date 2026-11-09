package connectors

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// feishuMaxContent bounds one document read; list results stop at 128 KiB.
const (
	feishuMaxContent  = 64 << 10
	feishuMaxResult   = 128 << 10
	feishuMaxPages    = 5
	feishuMaxItems    = 200
	feishuDefaultPage = 50
)

// feishuHandler serves the Feishu/Lark document and drive-comment tools.
type feishuHandler struct {
	doer    *Doer
	tokDoer *Doer
	cfg     FeishuSection
	sec     SecretSource
	tokens  *tokenCache
	red     *Redactor
	base    string
	now     func() time.Time
}

// FeishuSection is [modules.connectors.feishu].
type FeishuSection struct {
	Enabled      bool   `json:"enabled"`
	AppID        string `json:"app_id"`
	AppSecretEnv string `json:"app_secret_env"`
	BaseURL      string `json:"base_url"`
}

func (m *Module) newFeishu() (Handler, error) {
	cfg, err := decodeSub[FeishuSection](m.cfg.Feishu, "feishu")
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	if cfg.AppID == "" || cfg.AppSecretEnv == "" {
		return nil, &Error{Code: codeInvalidArgs, Message: "feishu needs app_id and app_secret_env"}
	}
	doer, err := m.newDoer(m.network.Client("feishu", netx.Tool))
	if err != nil {
		return nil, err
	}
	tokDoer, err := m.newDoer(m.network.Client("auth", netx.Inference))
	if err != nil {
		return nil, err
	}
	f := &feishuHandler{
		doer: doer, tokDoer: tokDoer, cfg: cfg, sec: m.deps.Secrets,
		red: m.redactor, base: feishuBase(cfg), now: m.deps.Now,
	}
	f.tokens = newTokenCache(f.refresh, m.deps.Now, m.redactor)
	return f, nil
}

func (f *feishuHandler) Tools() []string {
	return []string{
		"feishu_doc_read", "feishu_drive_list_comments",
		"feishu_drive_list_comment_replies", "feishu_drive_reply_comment", "feishu_drive_add_comment",
	}
}

func (f *feishuHandler) Invoke(ctx context.Context, tool string, raw json.RawMessage, _ host.Env) (host.Result, error) {
	tok, err := f.tokens.Token(ctx)
	if err != nil {
		return host.Result{}, err
	}
	switch tool {
	case "feishu_doc_read":
		return f.readDoc(ctx, tok, raw)
	case "feishu_drive_list_comments":
		return f.listComments(ctx, tok, raw, false)
	case "feishu_drive_list_comment_replies":
		return f.listComments(ctx, tok, raw, true)
	case "feishu_drive_reply_comment":
		return f.writeComment(ctx, tok, raw, true)
	case "feishu_drive_add_comment":
		return f.writeComment(ctx, tok, raw, false)
	}
	return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "unknown feishu tool " + tool}
}

func feishuBase(f FeishuSection) string {
	if f.BaseURL != "" {
		return strings.TrimRight(f.BaseURL, "/")
	}
	return "https://open.feishu.cn"
}

// refresh exchanges app credentials for a tenant access token.
func (f *feishuHandler) refresh(ctx context.Context) (token, error) {
	secret, err := ResolveSecret(f.sec, f.cfg.AppSecretEnv)
	if err != nil {
		return token{}, err
	}
	f.red.Add(secret)
	body, err := json.Marshal(map[string]string{"app_id": f.cfg.AppID, "app_secret": secret})
	if err != nil {
		return token{}, &Error{Code: codeInternal, Message: "encode feishu credentials", err: err}
	}
	var out struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if _, err := f.tokDoer.JSON(ctx, &Request{
		Method: http.MethodPost,
		URL:    joinURL(f.base, "/open-apis/auth/v3/tenant_access_token/internal"),
		Header: headerJSON(), Body: body, Accept: "application/json",
	}, &out); err != nil {
		return token{}, err
	}
	if out.Code != 0 || out.Token == "" {
		return token{}, &Error{Code: codeAuthRequired, Message: "feishu token rejected: " + out.Msg}
	}
	return token{Value: out.Token, Expiry: expiryOf(f.now(), out.Expire)}, nil
}

// readDoc returns the raw content of one document, clipped to 64 KiB.
func (f *feishuHandler) readDoc(ctx context.Context, tok string, raw json.RawMessage) (host.Result, error) {
	var a struct {
		DocumentID string `json:"document_id"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	segment, err := escapePath(a.DocumentID)
	if err != nil {
		return host.Result{}, err
	}
	data, err := f.get(ctx, tok, "/open-apis/docx/v1/documents/"+segment+"/raw_content")
	if err != nil {
		return host.Result{}, err
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "decode feishu document", err: err}
	}
	if out.Code != 0 {
		return host.Result{}, &Error{Code: codeDenied, Message: "feishu document: " + out.Msg}
	}
	content := out.Data.Content
	if len(content) > feishuMaxContent {
		content = content[:feishuMaxContent]
	}
	return host.Result{Content: content, UntrustedSource: "connector:feishu"}, nil
}

// listComments lists comments (or their replies) with bounded paging.
func (f *feishuHandler) listComments(ctx context.Context, tok string, raw json.RawMessage, replies bool) (host.Result, error) {
	var a struct {
		FileToken string `json:"file_token"`
		CommentID string `json:"comment_id"`
		Page      int    `json:"page"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	fileToken, err := escapePath(a.FileToken)
	if err != nil {
		return host.Result{}, err
	}
	path := "/open-apis/drive/v1/files/" + fileToken + "/comments"
	if replies {
		commentID, perr := escapePath(a.CommentID)
		if perr != nil {
			return host.Result{}, perr
		}
		path += "/" + commentID + "/replies"
	}
	pages, err := feishuPages(a.Page)
	if err != nil {
		return host.Result{}, err
	}
	items, truncated, err := f.pagedList(ctx, tok, path, pages)
	if err != nil {
		return host.Result{}, err
	}
	body, err := json.Marshal(struct {
		Items     []json.RawMessage `json:"items"`
		Truncated bool              `json:"truncated"`
	}{items, truncated})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode list", err: err}
	}
	return host.Result{Content: string(body), UntrustedSource: "connector:feishu"}, nil
}

func (f *feishuHandler) pagedList(ctx context.Context, tok, path string, pages int) ([]json.RawMessage, bool, error) {
	items := make([]json.RawMessage, 0, feishuDefaultPage)
	size := 0
	token := ""
	for range pages {
		query := "?page_size=" + strconv.Itoa(feishuDefaultPage)
		if token != "" {
			query += "&page_token=" + token
		}
		data, err := f.get(ctx, tok, path+query)
		if err != nil {
			return nil, false, err
		}
		var out struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items     []json.RawMessage `json:"items"`
				PageToken string            `json:"page_token"`
				HasMore   bool              `json:"has_more"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, false, &Error{Code: codeInternal, Message: "decode feishu list", err: err}
		}
		if out.Code != 0 {
			return nil, false, &Error{Code: codeDenied, Message: "feishu list: " + out.Msg}
		}
		for _, item := range out.Data.Items {
			items = append(items, item)
			size += len(item)
			if len(items) >= feishuMaxItems || size >= feishuMaxResult {
				return items, true, nil
			}
		}
		if !out.Data.HasMore || out.Data.PageToken == "" {
			break
		}
		token = out.Data.PageToken
	}
	return items, false, nil
}

// writeComment adds a comment or replies to one. Both are non-idempotent and
// therefore never retried.
func (f *feishuHandler) writeComment(ctx context.Context, tok string, raw json.RawMessage, reply bool) (host.Result, error) {
	var a struct {
		FileToken string `json:"file_token"`
		CommentID string `json:"comment_id"`
		Content   string `json:"content"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	if strings.TrimSpace(a.Content) == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "content must not be empty"}
	}
	fileToken, err := escapePath(a.FileToken)
	if err != nil {
		return host.Result{}, err
	}
	path := "/open-apis/drive/v1/files/" + fileToken + "/comments"
	if reply {
		commentID, perr := escapePath(a.CommentID)
		if perr != nil {
			return host.Result{}, perr
		}
		path += "/" + commentID + "/replies"
	}
	payload := map[string]any{"content": map[string]any{"elements": []any{
		map[string]any{"type": "text_run", "text_run": map[string]string{"text": a.Content}},
	}}}
	body, err := json.Marshal(payload)
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode comment", err: err}
	}
	data, err := f.call(ctx, http.MethodPost, path, tok, body)
	if err != nil {
		return host.Result{}, err
	}
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			CommentID string `json:"comment_id"`
		} `json:"data"`
	}
	if uerr := json.Unmarshal(data, &out); uerr != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "decode comment", err: uerr}
	}
	if out.Code != 0 {
		return host.Result{}, &Error{Code: codeDenied, Message: "feishu comment: " + out.Msg}
	}
	ack, err := json.Marshal(struct {
		CommentID string `json:"comment_id"`
	}{out.Data.CommentID})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode ack", err: err}
	}
	return host.Result{Content: string(ack), UntrustedSource: "connector:feishu"}, nil
}

func (f *feishuHandler) get(ctx context.Context, tok, path string) ([]byte, error) {
	return f.call(ctx, http.MethodGet, path, tok, nil)
}

// call runs one Feishu request; GET is the only retryable method.
func (f *feishuHandler) call(ctx context.Context, method, path, tok string, body []byte) ([]byte, error) {
	req := Request{
		Method: method, URL: joinURL(f.base, path), Header: bearerHeader(tok),
		Accept: "application/json", Retry: method == http.MethodGet, MaxBody: DefaultMaxBytes,
	}
	if body != nil {
		req.Body = body
		maps.Copy(req.Header, headerJSON())
	}
	var out json.RawMessage
	if _, err := f.doer.JSON(ctx, &req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// feishuPages clamps the page count of one list call.
func feishuPages(page int) (int, error) {
	if page < 0 || page > feishuMaxPages {
		return 0, &Error{Code: codeInvalidArgs, Message: "page must be 1..5"}
	}
	if page == 0 {
		page = 1
	}
	return page, nil
}
