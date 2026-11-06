package irc

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

type fakeIRC struct {
	ln       net.Listener
	caps     []string
	saslFail bool

	mu       sync.Mutex
	conns    int
	conn     net.Conn
	privmsgs []string
	writeMu  sync.Mutex
	joined   chan string
}

func newFakeIRC(t *testing.T, caps []string, saslFail bool) *fakeIRC {
	t.Helper()
	var listen net.ListenConfig
	ln, err := listen.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIRC{ln: ln, caps: caps, saslFail: saslFail, joined: make(chan string, 16)}
	go f.accept()
	t.Cleanup(f.Close)
	return f
}

func (f *fakeIRC) Addr() string { return f.ln.Addr().String() }

func (f *fakeIRC) Close() {
	if err := f.ln.Close(); err != nil {
		return
	}
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn != nil {
		if err := conn.Close(); err != nil {
			return
		}
	}
}

func (f *fakeIRC) accept() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns++
		f.conn = conn
		f.mu.Unlock()
		go f.serve(conn)
	}
}

func (f *fakeIRC) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	awaitingAuth := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "CAP LS"):
			f.write(conn, ":srv CAP * LS :"+strings.Join(f.caps, " "))
		case strings.HasPrefix(line, "CAP REQ"):
			f.write(conn, ":srv CAP * ACK :"+strings.TrimPrefix(line, "CAP REQ :"))
		case strings.HasPrefix(line, "AUTHENTICATE "):
			if f.saslFail {
				f.write(conn, ":srv 904 damnbot :SASL authentication failed")
				continue
			}
			if awaitingAuth {
				awaitingAuth = false
				f.write(conn, ":srv 903 damnbot :SASL authentication successful")
				continue
			}
			awaitingAuth = true
			f.write(conn, "AUTHENTICATE +")
		case strings.HasPrefix(line, "CAP END"):
			f.write(conn, ":srv 001 damnbot :Welcome")
		case strings.HasPrefix(line, "JOIN "):
			channel := strings.TrimPrefix(line, "JOIN ")
			f.write(conn, ":damnbot!u@h JOIN "+channel)
			f.write(conn, ":srv 366 damnbot "+channel+" :End of /NAMES list")
			f.joined <- channel
		case strings.HasPrefix(line, "PRIVMSG "):
			f.mu.Lock()
			f.privmsgs = append(f.privmsgs, line)
			f.mu.Unlock()
		}
	}
}

func (f *fakeIRC) write(conn net.Conn, line string) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	if _, err := conn.Write([]byte(line + "\r\n")); err != nil {
		return
	}
}

// Push sends one server line to the current connection.
func (f *fakeIRC) Push(line string) {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn != nil {
		f.write(conn, line)
	}
}

// Drop closes the current connection, forcing the adapter to reconnect.
func (f *fakeIRC) Drop() {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn != nil {
		if err := conn.Close(); err != nil {
			return
		}
	}
}

func (f *fakeIRC) Conns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func (f *fakeIRC) Privmsgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.privmsgs...)
}

func waitJoin(t *testing.T, f *fakeIRC) {
	t.Helper()
	select {
	case <-f.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("adapter never joined")
	}
}

func waitChan[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
	}
	var zero T
	return zero
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

func noDispatch(t *testing.T, ch <-chan *gateway.Inbound) {
	t.Helper()
	select {
	case in := <-ch:
		t.Fatalf("unexpected dispatch %+v", in)
	case <-time.After(100 * time.Millisecond):
	}
}

func descriptor(t *testing.T) *gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup("irc")
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func testAdapter(ctx context.Context, t *testing.T, f *fakeIRC, h gwplat.Handler, secrets gwplat.SecretSource) (*Adapter, *gwplat.Outbox) {
	t.Helper()
	d := descriptor(t)
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	t.Cleanup(sched.Close)
	out, err := gwplat.NewOutbox(d, sched)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := New(ctx, &Config{
		Enabled:       true,
		Addr:          f.Addr(),
		Nick:          "damnbot",
		Channels:      []string{"#chan"},
		SASLMechanism: MechanismPlain,
		SASLAccount:   "botaccount",
		Backoff:       gwplat.Backoff{Base: time.Millisecond, Max: 10 * time.Millisecond},
	}, d, secrets, out, h)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, out
}

// TestIRCAccountAuth is the GAP-SEC-10 IRC gate: no SASL or account-tag means
// module_unavailable and zero dispatch; a nickname is never an identity and a
// PRIVMSG without an account tag is dropped.
func TestIRCAccountAuth(t *testing.T) {
	password := secrets{"irc_sasl_password": "hunter2"}
	t.Run("no_sasl", func(t *testing.T) {
		f := newFakeIRC(t, []string{"account-tag"}, false)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var calls int
		h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error {
			calls++
			return nil
		})
		adapter, _ := testAdapter(ctx, t, f, h, password)
		err := adapter.Run(ctx)
		if !errors.Is(err, gateway.ErrIRCNoAccountAuth) || !errors.Is(err, gwplat.ErrUnavailable) {
			t.Fatalf("run = %v, want irc_no_account_auth module_unavailable", err)
		}
		if calls != 0 {
			t.Fatalf("dispatch without SASL: %d", calls)
		}
	})
	t.Run("no_account_tag", func(t *testing.T) {
		f := newFakeIRC(t, []string{"sasl"}, false)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		adapter, _ := testAdapter(ctx, t, f, gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }), password)
		if err := adapter.Run(ctx); !errors.Is(err, gateway.ErrIRCNoAccountAuth) {
			t.Fatalf("run = %v, want irc_no_account_auth", err)
		}
	})
	t.Run("sasl_rejected", func(t *testing.T) {
		f := newFakeIRC(t, []string{"sasl", "account-tag"}, true)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		adapter, _ := testAdapter(ctx, t, f, gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }), password)
		if err := adapter.Run(ctx); !errors.Is(err, gateway.ErrIRCNoAccountAuth) {
			t.Fatalf("run = %v, want irc_no_account_auth", err)
		}
	})
	t.Run("account_identity", testAccountIdentity)
	t.Run("activation_gate", testIRCActivationGate)
}

func testAccountIdentity(t *testing.T) {
	t.Helper()
	f := newFakeIRC(t, []string{"sasl", "account-tag", "account-notify"}, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handled := make(chan *gateway.Inbound, 8)
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled <- in
		return nil
	})
	adapter, _ := testAdapter(ctx, t, f, h, secrets{"irc_sasl_password": "hunter2"})
	go func() {
		if err := adapter.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("run: %v", err)
		}
	}()
	waitJoin(t, f)
	f.Push("@account=alice;msgid=a1 :mallory!u@h PRIVMSG #chan :hi damnbot")
	in := waitChan(t, handled, "account-tagged message")
	if in.SenderID != "alice" {
		t.Fatalf("sender = %q, want the account (not the nick)", in.SenderID)
	}
	if in.ChatID != "#chan" || !in.Group || !in.Mentioned {
		t.Fatalf("inbound = %+v", in)
	}
	f.Push("@account=alice;msgid=a1 :mallory!u@h PRIVMSG #chan :hi damnbot")
	noDispatch(t, handled)
	f.Push(":mallory!u@h PRIVMSG #chan :no account tag")
	noDispatch(t, handled)
	f.Push("@account=alice;msgid=a2 :mallory!u@h PRIVMSG damnbot :ping")
	dm := waitChan(t, handled, "dm")
	if dm.ChatID != "alice" || dm.Group {
		t.Fatalf("dm = %+v", dm)
	}
}

func testIRCActivationGate(t *testing.T) {
	t.Helper()
	f := newFakeIRC(t, []string{"sasl", "account-tag"}, false)
	ctx := t.Context()
	d := descriptor(t)
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	sched.Close()
	out, err := gwplat.NewOutbox(d, sched)
	if err != nil {
		t.Fatal(err)
	}
	password := secrets{"irc_sasl_password": "hunter2"}
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	if _, err := New(ctx, &Config{Addr: f.Addr(), Nick: "n", SASLAccount: "a"}, d, password, out, h); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want ErrUnavailable", err)
	}
	if _, err := New(ctx, &Config{Enabled: true, Addr: f.Addr(), Nick: "n", SASLAccount: "a"}, d, nil, out, h); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v, want ErrMissingSecret", err)
	}
	if _, err := New(ctx, &Config{Enabled: true, Addr: f.Addr(), Nick: "n", SASLAccount: "a", SASLMechanism: MechanismExternal}, d, nil, out, h); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("EXTERNAL without TLS = %v, want ErrUnavailable", err)
	}
	if got := f.Conns(); got != 0 {
		t.Fatalf("activation gate dialed %d times", got)
	}
}

// TestF7PLAT009IRC proves the adapter end to end: mention policy, outq/render
// outbound, replay dedupe, and reconnect without duplicate turns.
func TestF7PLAT009IRC(t *testing.T) {
	f := newFakeIRC(t, []string{"sasl", "account-tag", "account-notify"}, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handled := make(chan *gateway.Inbound, 8)
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled <- in
		return nil
	})
	adapter, out := testAdapter(ctx, t, f, h, secrets{"irc_sasl_password": "hunter2"})
	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx) }()
	waitJoin(t, f)

	f.Push("@account=alice;msgid=m1 :alice!u@h PRIVMSG #chan :hello everyone")
	noDispatch(t, handled)
	f.Push("@account=alice;msgid=m2 :alice!u@h PRIVMSG #chan :damnbot ping")
	first := waitChan(t, handled, "mentioned message")
	if first.UpdateID != "irc:m2" {
		t.Fatalf("update id = %q", first.UpdateID)
	}

	replyErr := out.Reply(ctx, gwplat.Reply{
		Chat:     "#chan",
		Group:    true,
		Markdown: "**pong**",
		Send:     func(ctx context.Context, text string) error { return adapter.Publish(ctx, "#chan", text) },
	})
	if replyErr != nil {
		t.Fatal(replyErr)
	}
	var sent string
	waitUntil(t, func() bool {
		msgs := f.Privmsgs()
		if len(msgs) == 0 {
			return false
		}
		sent = msgs[0]
		return true
	}, "outbound PRIVMSG")
	if !strings.HasPrefix(sent, "PRIVMSG #chan :") || strings.Contains(sent, "**") {
		t.Fatalf("outbound = %q", sent)
	}

	f.Drop()
	waitUntil(t, func() bool { return f.Conns() >= 2 }, "reconnect")
	waitJoin(t, f)
	f.Push("@account=alice;msgid=m2 :alice!u@h PRIVMSG #chan :damnbot ping")
	noDispatch(t, handled)
	f.Push("@account=alice;msgid=m3 :alice!u@h PRIVMSG #chan :damnbot again")
	second := waitChan(t, handled, "post-reconnect message")
	if second.UpdateID != "irc:m3" {
		t.Fatalf("update id = %q", second.UpdateID)
	}
	cancel()
	if stopErr := waitChan(t, runErr, "adapter stop"); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		t.Fatalf("run: %v", stopErr)
	}
}
