package connectors

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// tlsURL reports whether raw is an https/wss URL with a host.
func tlsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "wss") && u.Host != ""
}

// meetMaxCaption bounds one caption poll result.
const meetMaxCaption = 64 << 10

// meetHandler is the Google Meet bot node client of Q9. The join request
// carries the mandatory consent announcement; the node itself is Linux-only
// (browser + virtual PulseAudio/PipeWire), so every other GOOS answers
// module_unavailable.
type meetHandler struct {
	doer    *Doer
	cfg     MeetSection
	sec     SecretSource
	red     *Redactor
	base    string
	goos    string
	session map[string]bool
	mu      sync.Mutex
}

// MeetSection is [modules.connectors.meet].
type MeetSection struct {
	Enabled             bool   `json:"enabled"`
	NodeURL             string `json:"node_url"`
	NodeTokenEnv        string `json:"node_token_env"`
	ConsentText         string `json:"consent_text"`
	ConsentAnnouncement *bool  `json:"consent_announcement"`
	AckConsentAt        string `json:"ack_consent_responsibility_at"`
}

func (m *Module) newMeet() (Handler, error) {
	cfg, err := decodeSub[MeetSection](m.cfg.Meet, "meet")
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	if !tlsURL(cfg.NodeURL) && (m.deps.Manage == nil || !m.deps.Manage.InsecureTestNode) {
		return nil, &Error{Code: codeInvalidArgs, Message: "meet.node_url must be https or wss"}
	}
	announce := cfg.ConsentAnnouncement == nil || *cfg.ConsentAnnouncement
	if announce && strings.TrimSpace(cfg.ConsentText) == "" {
		return nil, &Error{Code: codeInvalidArgs, Message: "meet.consent_text must not be empty"}
	}
	if !announce && cfg.AckConsentAt == "" {
		return nil, &Error{Code: codeInvalidArgs, Message: "disabling consent needs ack_consent_responsibility_at"}
	}
	doer, err := m.newDoer(m.network.Client("meet", netx.Tool))
	if err != nil {
		return nil, err
	}
	return &meetHandler{
		doer: doer, cfg: cfg, sec: m.deps.Secrets, red: m.redactor,
		base: strings.TrimRight(cfg.NodeURL, "/"), goos: m.deps.GOOS,
		session: map[string]bool{},
	}, nil
}

func (mh *meetHandler) Tools() []string { return []string{"google_meet"} }

type meetArgs struct {
	Action     string `json:"action"`
	MeetingURL string `json:"meeting_url"`
	SessionID  string `json:"session_id"`
	Text       string `json:"text"`
	Since      int64  `json:"since"`
}

func (mh *meetHandler) Invoke(ctx context.Context, tool string, raw json.RawMessage, _ host.Env) (host.Result, error) {
	if tool != "google_meet" {
		return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "unknown meet tool " + tool}
	}
	if mh.goos != "linux" {
		return host.Result{}, &Error{
			Code:    codeModuleUnavailable,
			Message: "Google Meet bot requires Linux (browser + virtual audio)", Hint: "use a Linux host",
		}
	}
	var a meetArgs
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	tok, err := mh.token()
	if err != nil {
		return host.Result{}, err
	}
	switch a.Action {
	case "join":
		return mh.join(ctx, tok, a)
	case "transcript":
		return mh.transcript(ctx, tok, a)
	case "speak":
		return mh.speak(ctx, tok, a)
	case "leave":
		return mh.leave(ctx, tok, a)
	}
	return host.Result{}, &Error{Code: codeInvalidArgs, Message: "action must be join, transcript, speak or leave"}
}

// token resolves the optional node bearer. An unset node_token_env means the
// node is authenticated by its TLS client certificate instead.
func (mh *meetHandler) token() (string, error) {
	if mh.cfg.NodeTokenEnv == "" {
		return "", nil
	}
	tok, err := ResolveSecret(mh.sec, mh.cfg.NodeTokenEnv)
	if err != nil {
		return "", err
	}
	mh.red.Add(tok)
	return tok, nil
}

// join sends the meeting URL together with the consent announcement. The
// announcement is sent in the same request as the join, so it lands within
// the 10 s contract of GAP-GW-12.
func (mh *meetHandler) join(ctx context.Context, tok string, a meetArgs) (host.Result, error) {
	if !strings.HasPrefix(a.MeetingURL, "https://meet.google.com/") {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "meeting_url must be a https://meet.google.com/ URL"}
	}
	announce := mh.cfg.ConsentAnnouncement == nil || *mh.cfg.ConsentAnnouncement
	consent := strings.TrimSpace(mh.cfg.ConsentText)
	if announce && consent == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "consent announcement text is empty"}
	}
	payload := map[string]any{
		"meeting_url":          a.MeetingURL,
		"consent_announcement": announce,
		"consent_text":         consent,
	}
	if !announce {
		payload["ack_consent_responsibility_at"] = mh.cfg.AckConsentAt
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode meet join", err: err}
	}
	data, err := mh.call(ctx, http.MethodPost, "/join", tok, body)
	if err != nil {
		return host.Result{}, err
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.SessionID == "" {
		return host.Result{}, &Error{Code: codeInternal, Message: "meet node returned no session_id"}
	}
	mh.mu.Lock()
	mh.session[out.SessionID] = true
	mh.mu.Unlock()
	return host.Result{Content: string(data), UntrustedSource: "connector:meet"}, nil
}

func (mh *meetHandler) sessionPath(id, suffix string) (string, error) {
	segment, err := escapePath(id)
	if err != nil {
		return "", err
	}
	if !mh.known(id) {
		return "", &Error{Code: codeNotFound, Message: "unknown meet session " + id}
	}
	return "/sessions/" + segment + suffix, nil
}

func (mh *meetHandler) known(id string) bool {
	mh.mu.Lock()
	defer mh.mu.Unlock()
	return mh.session[id]
}

func (mh *meetHandler) transcript(ctx context.Context, tok string, a meetArgs) (host.Result, error) {
	path, err := mh.sessionPath(a.SessionID, "/captions")
	if err != nil {
		return host.Result{}, err
	}
	if a.Since > 0 {
		path += "?since=" + strconv.FormatInt(a.Since, 10)
	}
	data, err := mh.call(ctx, http.MethodGet, path, tok, nil)
	if err != nil {
		return host.Result{}, err
	}
	if len(data) > meetMaxCaption {
		data = data[:meetMaxCaption]
	}
	return host.Result{Content: string(data), UntrustedSource: "connector:meet"}, nil
}

func (mh *meetHandler) speak(ctx context.Context, tok string, a meetArgs) (host.Result, error) {
	if strings.TrimSpace(a.Text) == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "text must not be empty"}
	}
	path, err := mh.sessionPath(a.SessionID, "/speak")
	if err != nil {
		return host.Result{}, err
	}
	body, err := json.Marshal(map[string]string{"text": a.Text})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode speak", err: err}
	}
	data, err := mh.call(ctx, http.MethodPost, path, tok, body)
	if err != nil {
		return host.Result{}, err
	}
	return host.Result{Content: string(data), UntrustedSource: "connector:meet"}, nil
}

func (mh *meetHandler) leave(ctx context.Context, tok string, a meetArgs) (host.Result, error) {
	path, err := mh.sessionPath(a.SessionID, "/leave")
	if err != nil {
		return host.Result{}, err
	}
	data, err := mh.call(ctx, http.MethodPost, path, tok, []byte(`{}`))
	if err != nil {
		return host.Result{}, err
	}
	mh.mu.Lock()
	delete(mh.session, a.SessionID)
	mh.mu.Unlock()
	return host.Result{Content: string(data), UntrustedSource: "connector:meet"}, nil
}

func (mh *meetHandler) call(ctx context.Context, method, path, tok string, body []byte) ([]byte, error) {
	header := http.Header{}
	if tok != "" {
		maps.Copy(header, bearerHeader(tok))
	}
	if body != nil {
		maps.Copy(header, headerJSON())
	}
	var out json.RawMessage
	req := Request{
		Method: method, URL: joinURL(mh.base, path), Header: header,
		Accept: "application/json", Retry: method == http.MethodGet, MaxBody: DefaultMaxBytes,
	}
	req.Body = body
	if _, err := mh.doer.JSON(ctx, &req, &out); err != nil {
		return nil, err
	}
	return out, nil
}
