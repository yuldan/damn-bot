package mattermost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// TestMattermostValidateEndpoint keeps the token off plaintext non-loopback
// transports: only https or an explicit loopback http URL is accepted.
func TestMattermostValidateEndpoint(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"https://mm.example", true},
		{"https://mm.example/subpath", true},
		{"http://localhost:8065", true},
		{"http://127.0.0.1:8065", true},
		{"http://127.9.9.9:8065", true},
		{"http://mm.example", false},
		{"http://[::1]:8065", false},
		{"https://user:pw@mm.example", false},
		{"ftp://mm.example", false},
		{"mm.example", false},
		{"", false},
		{"://bad", false},
	} {
		err := validateEndpoint(tc.raw)
		if (err == nil) != tc.want {
			t.Errorf("validateEndpoint(%q) = %v, want ok=%v", tc.raw, err, tc.want)
		}
		if err != nil && !errors.Is(err, ErrConfig) {
			t.Errorf("validateEndpoint(%q) = %v, want ErrConfig", tc.raw, err)
		}
	}
}

// TestMattermostNewRejectsInvalidActivation covers descriptor, transport,
// seam and secret refusals of New without opening a connection.
func TestMattermostNewRejectsInvalidActivation(t *testing.T) {
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("mattermost")
	if err != nil {
		t.Fatal(err)
	}
	other := desc
	other.Name = "slack"
	out := newOutbox(t)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	okCfg := func() *Config { return &Config{Enabled: true, BaseURL: "https://mm.example"} }
	cases := []struct {
		name string
		cfg  *Config
		d    *gwplat.Descriptor
		o    *gwplat.Outbox
		h    gwplat.Handler
		src  gwplat.SecretSource
		want error
	}{
		{"nil config", nil, &desc, out, h, secrets{"mattermost_token": "t"}, ErrConfig},
		{"nil descriptor", okCfg(), nil, out, h, secrets{"mattermost_token": "t"}, ErrConfig},
		{"foreign descriptor", okCfg(), &other, out, h, secrets{"mattermost_token": "t"}, ErrConfig},
		{"disabled", &Config{}, &desc, out, h, secrets{"mattermost_token": "t"}, gwplat.ErrUnavailable},
		{"plaintext remote", &Config{Enabled: true, BaseURL: "http://mm.example"}, &desc, out, h, secrets{"mattermost_token": "t"}, ErrConfig},
		{"nil outbox", okCfg(), &desc, nil, h, secrets{"mattermost_token": "t"}, ErrConfig},
		{"nil handler", okCfg(), &desc, out, nil, secrets{"mattermost_token": "t"}, ErrConfig},
		{"missing secret", okCfg(), &desc, out, h, secrets{}, gwplat.ErrMissingSecret},
	}
	for _, tc := range cases {
		if _, err := New(t.Context(), tc.cfg, tc.d, tc.src, tc.o, tc.h); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestMattermostSessionDerivesWSAndFiltersEvents drives the production WS
// URL derivation (/api/v4/websocket) and rejects frames that are not a posted
// message before dispatch.
func TestMattermostSessionDerivesWSAndFiltersEvents(t *testing.T) {
	var path string
	events := make(chan struct{}, 1)
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		ws, aerr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if aerr != nil {
			return
		}
		defer func() {
			if cerr := ws.CloseNow(); cerr != nil {
				return
			}
		}()
		serveMattermostFrames(r.Context(), ws, events)
	}))
	t.Cleanup(wsSrv.Close)
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("mattermost")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan gateway.Inbound, 2)
	a, err := New(t.Context(), &Config{
		Enabled: true, BaseURL: wsSrv.URL,
		// No WSURL: the adapter derives ws://<host>/api/v4/websocket.
	}, &desc, secrets{"mattermost_token": "t"}, newOutbox(t),
		gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error { got <- *in; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if rerr := a.Run(ctx); rerr != nil {
			return
		}
	}()
	in := awaitInbound(t, got)
	if in.UpdateID != "p1" || in.ChatID != "c1" || in.Text != "halo" {
		t.Fatalf("inbound = %+v", in)
	}
	select {
	case extra := <-got:
		t.Fatalf("replayed/foreign frame dispatched: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("ws server never finished")
	}
	if path != "/api/v4/websocket" {
		t.Fatalf("derived ws path = %q", path)
	}
}

// TestMattermostDispatchErrorEndsSession: a handler failure is not swallowed;
// it ends the websocket session so the core can answer the platform.
func TestMattermostDispatchErrorEndsSession(t *testing.T) {
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		frame := `{"event":"posted","data":{"post":"{\"id\":\"p2\",\"message\":\"x\",\"channel_id\":\"c1\",\"user_id\":\"u1\"}"}}`
		if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			return
		}
		<-ctx.Done()
	})
	t.Cleanup(wsSrv.Close)
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("mattermost")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(t.Context(), &Config{
		Enabled: true, BaseURL: wsSrv.URL, WSURL: wsURLOf(wsSrv),
	}, &desc, secrets{"mattermost_token": "t"}, newOutbox(t),
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return errors.New("boom") }))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.session(t.Context()); err == nil || !strings.Contains(err.Error(), "dispatch") {
		t.Fatalf("session = %v, want dispatch error", err)
	}
}

// TestMattermostRunReconnectBackoff: a dial failure is retried with the
// configured backoff until the context ends.
func TestMattermostRunReconnectBackoff(t *testing.T) {
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("mattermost")
	if err != nil {
		t.Fatal(err)
	}
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	a, err := New(t.Context(), &Config{
		Enabled: true, BaseURL: "https://mm.example", WSURL: "ws" + strings.TrimPrefix(url, "http"),
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond},
	}, &desc, secrets{"mattermost_token": "t"}, newOutbox(t),
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := a.Run(ctx); err == nil {
		t.Fatal("run with a dead endpoint returned nil")
	}
}

// TestMattermostSendRefusesEmptyChat: the outbox is the only send path and it
// refuses a reply without a channel before touching the network.
func TestMattermostSendRefusesEmptyChat(t *testing.T) {
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("mattermost")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(t.Context(), &Config{Enabled: true, BaseURL: "https://mm.example"},
		&desc, secrets{"mattermost_token": "t"}, newOutbox(t),
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if serr := a.Send(t.Context(), "", "hi"); !errors.Is(serr, gwplat.ErrNoReply) {
		t.Fatalf("empty chat = %v, want ErrNoReply", serr)
	}
}

func wsURLOf(srv *httptest.Server) string { return "ws" + strings.TrimPrefix(srv.URL, "http") }

// serveMattermostFrames writes one frame of each rejected shape, one admitted
// post and its replay, then blocks until the session context ends.
func serveMattermostFrames(ctx context.Context, ws *websocket.Conn, done chan<- struct{}) {
	if _, _, err := ws.Read(ctx); err != nil {
		return
	}
	frames := []string{
		`not-json`,
		`{"event":"typing","data":{}}`,
		`{"event":"posted","data":{"post":"not-json"}}`,
		`{"event":"posted","data":{"post":"{\"id\":\"\",\"channel_id\":\"c1\"}"}}`,
		`{"event":"posted","data":{"post":"{\"id\":\"p1\",\"message\":\"halo\",\"channel_id\":\"c1\",\"user_id\":\"u1\"}"}}`,
		`{"event":"posted","data":{"post":"{\"id\":\"p1\",\"message\":\"halo\",\"channel_id\":\"c1\",\"user_id\":\"u1\"}"}}`,
	}
	for _, frame := range frames {
		if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			return
		}
	}
	done <- struct{}{}
	<-ctx.Done()
}
