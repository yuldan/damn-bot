package photon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// photonDesc returns the registered descriptor for the negative-path tests.
func photonDesc(t *testing.T) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("photon")
	if err != nil {
		t.Fatal(err)
	}
	return desc
}

// photonOutbox builds the outbox of an enabled adapter without a listener.
func photonOutbox(t *testing.T, desc *gwplat.Descriptor) *gwplat.Outbox {
	t.Helper()
	out, err := gwplat.NewOutbox(desc, newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPhotonNewRejectsInvalidActivation covers every fail-closed branch of New
// before the adapter can poll: descriptor, enablement, seams and secrets.
func TestPhotonNewRejectsInvalidActivation(t *testing.T) {
	desc := photonDesc(t)
	other := desc
	other.Name = "slack"
	out := photonOutbox(t, &desc)
	h := newRecorder()
	okPresent := func() error { return nil }
	okCfg := func() *Config {
		return &Config{Enabled: true, BaseURL: "http://127.0.0.1:9", ProjectID: "proj", Present: okPresent}
	}
	cases := []struct {
		name string
		cfg  *Config
		d    *gwplat.Descriptor
		o    *gwplat.Outbox
		h    gwplat.Handler
		src  gwplat.SecretSource
		want error
	}{
		{"nil config", nil, &desc, out, h, secrets{"photon_project_secret": "s"}, ErrConfig},
		{"nil descriptor", okCfg(), nil, out, h, secrets{"photon_project_secret": "s"}, ErrConfig},
		{"foreign descriptor", okCfg(), &other, out, h, secrets{"photon_project_secret": "s"}, ErrConfig},
		{"disabled", &Config{Present: okPresent}, &desc, out, h, secrets{"photon_project_secret": "s"}, gwplat.ErrUnavailable},
		{"nil outbox", okCfg(), &desc, nil, h, secrets{"photon_project_secret": "s"}, ErrConfig},
		{"nil handler", okCfg(), &desc, out, nil, secrets{"photon_project_secret": "s"}, ErrConfig},
		{"missing secret", okCfg(), &desc, out, h, secrets{}, gwplat.ErrMissingSecret},
	}
	for _, tc := range cases {
		if _, err := New(t.Context(), tc.cfg, tc.d, tc.src, tc.o, tc.h); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestPhotonProbeIsZeroNetwork enforces the sidecar presence contract: the
// probe never dials, an absent socket is module_unavailable, and a unix
// sidecar without an injected client is refused (E-05).
func TestPhotonProbeIsZeroNetwork(t *testing.T) {
	desc := photonDesc(t)
	out := photonOutbox(t, &desc)
	h := newRecorder()
	socket := filepath.Join(t.TempDir(), "sidecar.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cfg  *Config
		want error
	}{
		{"missing project id", &Config{Enabled: true, BaseURL: "http://127.0.0.1:9"}, ErrConfig},
		{"non-loopback host", &Config{Enabled: true, ProjectID: "p", BaseURL: "http://example.com"}, ErrConfig},
		{"non-http scheme", &Config{Enabled: true, ProjectID: "p", BaseURL: "https://127.0.0.1:9"}, ErrConfig},
		{"userinfo", &Config{Enabled: true, ProjectID: "p", BaseURL: "http://" + "user" + ":" + "pw" + "@127.0.0.1:9"}, ErrConfig},
		{"malformed url", &Config{Enabled: true, ProjectID: "p", BaseURL: "://bad"}, ErrConfig},
		{"absent socket", &Config{Enabled: true, ProjectID: "p", SocketPath: filepath.Join(t.TempDir(), "none.sock")}, ErrSidecarAbsent},
		{"socket without client", &Config{Enabled: true, ProjectID: "p", SocketPath: socket}, ErrConfig},
	}
	for _, tc := range cases {
		_, err := New(t.Context(), tc.cfg, &desc, secrets{"photon_project_secret": "s"}, out, h)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
		if errors.Is(tc.want, ErrSidecarAbsent) && !errors.Is(err, gwplat.ErrUnavailable) {
			t.Errorf("%s: absent sidecar lacks module_unavailable: %v", tc.name, err)
		}
	}
	cfg := &Config{Enabled: true, ProjectID: "p", SocketPath: socket, HTTPClient: netx.LoopbackClient()}
	if _, err := New(t.Context(), cfg, &desc, secrets{"photon_project_secret": "s"}, out, h); err != nil {
		t.Fatalf("unix sidecar with client: %v", err)
	}
}

// TestPhotonPollStatusAndDecodeErrors: a non-2xx and a corrupt body both fail
// the poll and the send instead of admitting anything.
func TestPhotonPollStatusAndDecodeErrors(t *testing.T) {
	batch := func(status int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			if _, err := w.Write([]byte(body)); err != nil {
				return
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	for _, tc := range []struct {
		name string
		srv  *httptest.Server
		want string
	}{
		{"status", batch(http.StatusInternalServerError, `{}`), "status 500"},
		{"decode", batch(http.StatusOK, `not-json`), "decode"},
	} {
		ad, err := buildAdapter(t, &Config{
			Enabled: true, BaseURL: tc.srv.URL, ProjectID: "p",
			HTTPClient: netx.LoopbackClient(), Present: func() error { return nil },
		},
			secrets{"photon_project_secret": "s"}, newRecorder(), newScheduler(t))
		if err != nil {
			t.Fatal(err)
		}
		perr := ad.poll(t.Context())
		if perr == nil || !strings.Contains(perr.Error(), tc.want) {
			t.Fatalf("%s: poll = %v, want %q", tc.name, perr, tc.want)
		}
		if serr := ad.sendText("c1")(t.Context(), "hi"); serr == nil {
			t.Fatalf("%s: send to a failing sidecar succeeded", tc.name)
		}
	}
}

// TestPhotonTransportAbsent: a dead sidecar is module_unavailable and Run
// stops instead of reconnecting.
func TestPhotonTransportAbsent(t *testing.T) {
	ad, err := buildAdapter(t, &Config{
		Enabled: true, BaseURL: "http://127.0.0.1:1", ProjectID: "p",
		Present: func() error { return nil },
	}, secrets{"photon_project_secret": "s"}, newRecorder(), newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if perr := ad.poll(t.Context()); !errors.Is(perr, ErrSidecarAbsent) {
		t.Fatalf("poll transport = %v, want ErrSidecarAbsent", perr)
	}
	if rerr := ad.Run(t.Context()); !errors.Is(rerr, ErrSidecarAbsent) {
		t.Fatalf("run = %v, want ErrSidecarAbsent", rerr)
	}
}

// TestPhotonSendRefusesEmptyChat: the outbox rejects a reply without a chat
// before any sidecar call.
func TestPhotonSendRefusesEmptyChat(t *testing.T) {
	ad, err := buildAdapter(t, &Config{
		Enabled: true, BaseURL: "http://127.0.0.1:9", ProjectID: "p",
		Present: func() error { return nil },
	}, secrets{"photon_project_secret": "s"}, newRecorder(), newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if serr := ad.Send(t.Context(), "", "hi"); !errors.Is(serr, gwplat.ErrNoReply) {
		t.Fatalf("empty chat = %v, want ErrNoReply", serr)
	}
}

// TestPhotonDispatchGates covers the inbound admission gates (empty id/text,
// duplicate ids, group mention policy) and a handler failure.
func TestPhotonDispatchGates(t *testing.T) {
	batch := `{"messages":[` +
		`{"id":"","chat":"c1","from":"a1","text":"no id"},` +
		`{"id":"p9","chat":"c1","from":"a1","text":"  "},` +
		`{"id":"p8","chat":"g1","from":"a1","text":"no mention","group":true},` +
		`{"id":"p7","chat":"c1","from":"a1","text":"hello"}]}`
	sidecar := newFakeSidecar(t, batch)
	rec := newRecorder()
	ad, err := buildAdapter(t, &Config{
		Enabled: true, BaseURL: sidecar.srv.URL, ProjectID: "p",
		HTTPClient: netx.LoopbackClient(), Present: func() error { return nil },
	},
		secrets{"photon_project_secret": "s"}, rec, newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if perr := ad.poll(t.Context()); perr != nil {
		t.Fatal(perr)
	}
	if rec.count() != 1 {
		t.Fatalf("admitted = %d, want 1 (empty/blank/unmentioned dropped)", rec.count())
	}
	// A fresh adapter sees a fresh id; the first poll already claimed p7.
	failingSidecar := newFakeSidecar(t, `{"messages":[{"id":"p6","chat":"c1","from":"a1","text":"hello"}]}`)
	failing, err := buildAdapter(t, &Config{
		Enabled: true, BaseURL: failingSidecar.srv.URL, ProjectID: "p",
		HTTPClient: netx.LoopbackClient(), Present: func() error { return nil },
	},
		secrets{"photon_project_secret": "s"},
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return errors.New("boom") }),
		newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if perr := failing.poll(t.Context()); perr == nil || !strings.Contains(perr.Error(), "dispatch") {
		t.Fatalf("handler failure = %v", perr)
	}
}

// TestPhotonRunStopsOnPollFailure: a failing poll followed by a cancelled
// context ends Run cleanly after the warn, without a reconnect.
func TestPhotonRunStopsOnPollFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	requested := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case requested <- struct{}{}:
		default:
		}
		cancel()
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	ad, err := buildAdapter(t, &Config{
		Enabled: true, BaseURL: srv.URL, ProjectID: "p",
		HTTPClient: netx.LoopbackClient(), Present: func() error { return nil },
	},
		secrets{"photon_project_secret": "s"}, newRecorder(), newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if rerr := ad.Run(ctx); rerr != nil {
		t.Fatalf("run = %v, want nil after cancellation", rerr)
	}
	select {
	case <-requested:
	case <-time.After(5 * time.Second):
		t.Fatal("run never polled")
	}
}
