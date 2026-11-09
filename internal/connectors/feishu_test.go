package connectors

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func feishuModule(t *testing.T, srv *httptest.Server) *Module {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"enabled": true, "app_id": "app", "app_secret_env": "FEISHU_SECRET", "base_url": srv.URL,
	})
	if err != nil {
		t.Fatalf("feishu raw: %v", err)
	}
	cfg := Config{Enabled: true, Timeout: 5 * time.Second, Feishu: raw}
	m := New(&cfg, Deps{Secrets: testSecrets, Now: time.Now})
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return m
}

func TestF8TOOL040FeishuDocRead(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			writeBody(t, w, `{"code":0,"tenant_access_token":"feishu-token-123456","expire":7200}`)
		case "/open-apis/docx/v1/documents/doc-1/raw_content":
			if got := r.Header.Get("Authorization"); got != "Bearer feishu-token-123456" {
				t.Errorf("authorization = %q", got)
			}
			writeBody(t, w, `{"code":0,"data":{"content":"hello world"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := feishuModule(t, srv)
	res, err := m.Invoke(t.Context(), "feishu_doc_read", json.RawMessage(`{"document_id":"doc-1"}`), host.Env{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.Content != "hello world" || res.UntrustedSource != "connector:feishu" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := m.Invoke(t.Context(), "feishu_doc_read", json.RawMessage(`{"document_id":""}`), host.Env{}); err == nil {
		t.Fatal("empty document id was accepted")
	}
}

func TestF8TOOL043FeishuListComments(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "tenant_access_token/internal"):
			writeBody(t, w, `{"code":0,"tenant_access_token":"feishu-token-123456","expire":7200}`)
		case strings.HasSuffix(r.URL.Path, "/files/file-1/comments"):
			if got := r.URL.Query().Get("page_size"); got != "50" {
				t.Errorf("page_size = %q", got)
			}
			if calls.Add(1) == 1 {
				writeBody(t, w, `{"code":0,"data":{"items":[{"comment_id":"c1"}],"page_token":"p2","has_more":true}}`)
				return
			}
			writeBody(t, w, `{"code":0,"data":{"items":[{"comment_id":"c2"}],"has_more":false}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := feishuModule(t, srv)
	res, err := m.Invoke(t.Context(), "feishu_drive_list_comments",
		json.RawMessage(`{"file_token":"file-1","page":2}`), host.Env{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var out struct {
		Items []map[string]string `json:"items"`
	}
	if uerr := json.Unmarshal([]byte(res.Content), &out); uerr != nil || len(out.Items) != 2 || calls.Load() != 2 {
		t.Fatalf("result = %q calls %d err %v", res.Content, calls.Load(), uerr)
	}
	// Feishu's own permission code is a typed denial, never silently empty.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "tenant_access_token/internal") {
			writeBody(t, w, `{"code":0,"tenant_access_token":"feishu-token-123456","expire":7200}`)
			return
		}
		writeBody(t, w, `{"code":99991663,"msg":"no permission"}`)
	}))
	defer srv2.Close()
	_, err = feishuModule(t, srv2).Invoke(t.Context(), "feishu_drive_list_comments",
		json.RawMessage(`{"file_token":"file-1"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" {
		t.Fatalf("permission = %v", err)
	}
}

func TestF8TOOL042FeishuListReplies(t *testing.T) {
	t.Parallel()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "tenant_access_token/internal"):
			writeBody(t, w, `{"code":0,"tenant_access_token":"feishu-token-123456","expire":7200}`)
		default:
			path = r.URL.Path
			writeBody(t, w, `{"code":0,"data":{"items":[],"has_more":false}}`)
		}
	}))
	defer srv.Close()
	m := feishuModule(t, srv)
	if _, err := m.Invoke(t.Context(), "feishu_drive_list_comment_replies",
		json.RawMessage(`{"file_token":"file-1","comment_id":"c1","page":1}`), host.Env{}); err != nil {
		t.Fatalf("replies: %v", err)
	}
	if path != "/open-apis/drive/v1/files/file-1/comments/c1/replies" {
		t.Fatalf("path = %q", path)
	}
}

func TestF8TOOL041FeishuAddComment(t *testing.T) {
	t.Parallel()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "tenant_access_token/internal"):
			writeBody(t, w, `{"code":0,"tenant_access_token":"feishu-token-123456","expire":7200}`)
		default:
			if r.Method != http.MethodPost {
				t.Errorf("method = %s", r.Method)
			}
			decodeJSON(t, r.Body, &body)
			writeBody(t, w, `{"code":0,"data":{"comment_id":"c-new"}}`)
		}
	}))
	defer srv.Close()
	m := feishuModule(t, srv)
	res, err := m.Invoke(t.Context(), "feishu_drive_add_comment",
		json.RawMessage(`{"file_token":"file-1","content":"please review"}`), host.Env{})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(res.Content, "c-new") {
		t.Fatalf("ack = %q", res.Content)
	}
	if !strings.Contains(mustJSON(t, body), "please review") {
		t.Fatalf("body = %v", body)
	}
	if _, err := m.Invoke(t.Context(), "feishu_drive_add_comment",
		json.RawMessage(`{"file_token":"file-1","content":"  "}`), host.Env{}); err == nil {
		t.Fatal("empty comment was accepted")
	}
}

func TestF8TOOL044FeishuReplyComment(t *testing.T) {
	t.Parallel()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "tenant_access_token/internal"):
			writeBody(t, w, `{"code":0,"tenant_access_token":"feishu-token-123456","expire":7200}`)
		default:
			path = r.URL.Path
			writeBody(t, w, `{"code":0,"data":{"comment_id":"c-reply"}}`)
		}
	}))
	defer srv.Close()
	m := feishuModule(t, srv)
	if _, err := m.Invoke(t.Context(), "feishu_drive_reply_comment",
		json.RawMessage(`{"file_token":"file-1","comment_id":"c1","content":"ok"}`), host.Env{}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if path != "/open-apis/drive/v1/files/file-1/comments/c1/replies" {
		t.Fatalf("path = %q", path)
	}
	if _, err := m.Invoke(t.Context(), "feishu_drive_reply_comment",
		json.RawMessage(`{"file_token":"file-1","comment_id":"c/../x","content":"ok"}`), host.Env{}); err != nil {
		t.Fatalf("escaped comment id was rejected: %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}
