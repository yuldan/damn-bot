package email

import (
	"bufio"
	"context"
	"errors"
	"fmt"
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

type recorder struct {
	mu   sync.Mutex
	in   []gateway.Inbound
	seen chan gateway.Inbound
}

func newRecorder() *recorder { return &recorder{seen: make(chan gateway.Inbound, 8)} }

func (r *recorder) Handle(_ context.Context, in *gateway.Inbound) error {
	r.mu.Lock()
	r.in = append(r.in, *in)
	r.mu.Unlock()
	r.seen <- *in
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.in)
}

type memCursor struct {
	mu    sync.Mutex
	c     Cursor
	found bool
	saves int
}

func (m *memCursor) Load(context.Context) (Cursor, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c, m.found, nil
}

func (m *memCursor) Save(_ context.Context, c Cursor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c, m.found = c, true
	m.saves++
	return nil
}

func (m *memCursor) get() Cursor {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c
}

type mailItem struct {
	uid uint32
	raw string
}

// fakeMailbox speaks IMAP4rev1 over net.Pipe with a fixed message set, one
// UIDVALIDITY generation and a scripted IDLE.
type fakeMailbox struct {
	conn        net.Conn
	uidValidity uint32
	mail        []mailItem
	mu          sync.Mutex
	cmds        []string
	logins      int
	idles       int
	stores      []string
	wg          sync.WaitGroup
}

func newFakeMailbox(t *testing.T, uidValidity uint32, mail ...mailItem) *fakeMailbox {
	t.Helper()
	f := &fakeMailbox{uidValidity: uidValidity, mail: mail}
	client, server := net.Pipe()
	f.conn = client
	f.wg.Go(func() {
		f.serve(server)
	})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			return
		}
		f.wg.Wait()
	})
	return f
}

func (f *fakeMailbox) dial(context.Context, string, string) (net.Conn, error) { return f.conn, nil }

func (f *fakeMailbox) record(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, line)
	switch {
	case strings.Contains(line, " LOGIN "):
		f.logins++
	case strings.HasPrefix(line, "a") && strings.Contains(line, "UID STORE"):
		f.stores = append(f.stores, line)
	}
}

func (f *fakeMailbox) snapshot() (cmds []string, logins, idles int, stores []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...), f.logins, f.idles, append([]string(nil), f.stores...)
}

func (f *fakeMailbox) serve(server net.Conn) {
	defer func() {
		if err := server.Close(); err != nil {
			return
		}
	}()
	w := bufio.NewWriter(server)
	r := bufio.NewReader(server)
	send := func(text string) bool {
		if _, err := w.WriteString(text); err != nil {
			return false
		}
		if err := w.Flush(); err != nil {
			return false
		}
		return true
	}
	if !send("* OK fake ready\r\n") {
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		tag := fields[0]
		if !f.respond(send, tag, line, r) {
			return
		}
	}
}

func (f *fakeMailbox) idle(send func(string) bool, tag string, r *bufio.Reader) bool {
	f.mu.Lock()
	f.idles++
	f.mu.Unlock()
	if !send("+ idling\r\n") || !send("* 1 EXISTS\r\n") {
		return false
	}
	done, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	if !strings.HasPrefix(strings.TrimSpace(done), "DONE") {
		return false
	}
	return send(tag + " OK IDLE terminated\r\n")
}

func (f *fakeMailbox) respond(send func(string) bool, tag, line string, r *bufio.Reader) bool {
	switch {
	case strings.Contains(line, " LOGIN "):
		return send(tag + " OK LOGIN completed\r\n")
	case strings.Contains(line, " SELECT "):
		return send(fmt.Sprintf("* %d EXISTS\r\n* OK [UIDVALIDITY %d] valid\r\n* OK [HIGHESTMODSEQ 99] ok\r\n%s OK [READ-WRITE] SELECT completed\r\n",
			len(f.mail), f.uidValidity, tag))
	case strings.Contains(line, "UID FETCH") && strings.HasSuffix(line, "(UID)"):
		var b strings.Builder
		for i, m := range f.mail {
			fmt.Fprintf(&b, "* %d FETCH (UID %d)\r\n", i+1, m.uid)
		}
		b.WriteString(tag + " OK FETCH completed\r\n")
		return send(b.String())
	case strings.Contains(line, "UID FETCH"):
		for _, m := range f.mail {
			if !strings.Contains(line, fmt.Sprintf("UID FETCH %d ", m.uid)) {
				continue
			}
			return send(fmt.Sprintf("* 1 FETCH (UID %d BODY[] {%d}\r\n%s)\r\n%s OK FETCH completed\r\n", m.uid, len(m.raw), m.raw, tag))
		}
		return send(tag + " OK FETCH completed\r\n")
	case strings.Contains(line, "UID STORE"):
		return send(tag + " OK STORE completed\r\n")
	case strings.HasSuffix(line, " IDLE"):
		return f.idle(send, tag, r)
	case strings.Contains(line, "LOGOUT"):
		return send("* BYE\r\n" + tag + " OK LOGOUT completed\r\n")
	default:
		return send(tag + " BAD unknown\r\n")
	}
}

// fakeSMTP accepts one message per connection and records the DATA payload.
type fakeSMTP struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeSMTP) dial(_ context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	go f.serve(server)
	return client, nil
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer func() {
		if err := conn.Close(); err != nil {
			return
		}
	}()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	say := func(text string) bool {
		if _, err := w.WriteString(text); err != nil {
			return false
		}
		if err := w.Flush(); err != nil {
			return false
		}
		return true
	}
	if !say("220 fake ESMTP\r\n") {
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			say("250-fake\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(line, "AUTH PLAIN"):
			say("235 2.7.0 authenticated\r\n")
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			say("250 2.1.0 ok\r\n")
		case strings.HasPrefix(line, "DATA"):
			say("354 go ahead\r\n")
			if !f.data(r, say) {
				return
			}
		case strings.HasPrefix(line, "QUIT"):
			say("221 bye\r\n")
			return
		default:
			say("250 ok\r\n")
		}
	}
}

// data reads one dot-terminated payload and records it.
func (f *fakeSMTP) data(r *bufio.Reader, say func(string) bool) bool {
	var body strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return false
		}
		if line == ".\r\n" {
			break
		}
		body.WriteString(line)
	}
	f.mu.Lock()
	f.msgs = append(f.msgs, body.String())
	f.mu.Unlock()
	return say("250 2.0.0 queued\r\n")
}

func (f *fakeSMTP) first() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		return ""
	}
	return f.msgs[0]
}

func newScheduler(t *testing.T) *outq.Scheduler {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	return sched
}

func buildAdapter(t *testing.T, cfg *Config, src gwplat.SecretSource, h gwplat.Handler, sched *outq.Scheduler) (*Adapter, error) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	desc, err := reg.Lookup("email")
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		return nil, fmt.Errorf("outbox: %w", err)
	}
	ad, err := New(t.Context(), cfg, &desc, src, out, h)
	if err != nil {
		return nil, fmt.Errorf("new: %w", err)
	}
	return ad, nil
}

func mustAdapter(t *testing.T, cfg *Config, h gwplat.Handler) *Adapter {
	t.Helper()
	ad, err := buildAdapter(t, cfg, secrets{"email_password": "pw"}, h, newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	return ad
}

func mailConfig(mb *fakeMailbox) *Config {
	return &Config{
		Enabled: true, Address: "bot@example.com", IMAPAddr: "fake:143", SMTPAddr: "fake:25",
		Timeout: 5 * time.Second, Dial: mb.dial, Wait: func(context.Context, time.Duration) error { return nil },
	}
}

// TestF7PLAT006Email covers the PLAT-006 row: explicit activation, dedupe
// Message-ID, mention policy with rejected mail left UNSEEN, and the SMTP reply.
func TestF7PLAT006Email(t *testing.T) {
	t.Run("activation_gate", testEmailActivation)
	t.Run("inbound_and_reply", testEmailInboundAndReply)
	t.Run("rejected_stays_unseen", testEmailRejectedUnseen)
}

func testEmailActivation(t *testing.T) {
	mb := newFakeMailbox(t, 7)
	cfg := mailConfig(mb)
	if _, err := buildAdapter(t, cfg, secrets{}, newRecorder(), newScheduler(t)); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v, want ErrMissingSecret", err)
	}
	if _, logins, _, _ := mb.snapshot(); logins != 0 {
		t.Fatalf("missing secret opened %d IMAP sessions", logins)
	}
}

func testEmailInboundAndReply(t *testing.T) {
	raw := "Message-ID: <m1@example.com>\r\nFrom: Alice <alice@example.com>\r\nTo: bot@example.com\r\nSubject: hi\r\n\r\nhello bot@example.com\r\n"
	mb := newFakeMailbox(t, 7, mailItem{uid: 5, raw: raw})
	smtp := &fakeSMTP{}
	cfg := mailConfig(mb)
	cfg.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasSuffix(addr, ":25") {
			return smtp.dial(ctx, network, addr)
		}
		return mb.dial(ctx, network, addr)
	}
	rec := newRecorder()
	ad := mustAdapter(t, cfg, rec)
	if err := ad.pollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-rec.seen:
		if in.Platform != "email" || in.ChatID != "alice@example.com" || in.SenderID != "alice@example.com" ||
			in.UpdateID != "email:<m1@example.com>" || !strings.Contains(in.Text, "hello") {
			t.Fatalf("inbound = %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound")
	}
	_, _, _, stores := mb.snapshot()
	if len(stores) != 1 || !strings.Contains(stores[0], "UID STORE 5") {
		t.Fatalf("stores = %v, want one UID STORE 5", stores)
	}
	assertEmailReply(t, ad, smtp)
}

func assertEmailReply(t *testing.T, ad *Adapter, smtp *fakeSMTP) {
	t.Helper()
	if err := ad.Send(t.Context(), "alice@example.com", "hello **world**", "Re: hi", "m1@example.com"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for smtp.first() == "" && time.Now().Before(deadline) {
		select {
		case <-t.Context().Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	msg := smtp.first()
	for _, want := range []string{"From: bot@example.com", "To: alice@example.com", "Subject: Re: hi", "In-Reply-To: <m1@example.com>", "hello world"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("SMTP message lacks %q:\n%s", want, msg)
		}
	}
}

func testEmailRejectedUnseen(t *testing.T) {
	raw := "Message-ID: <list1@example.com>\r\nFrom: Alice <alice@example.com>\r\nSubject: list\r\nList-Id: Team <team.example.com>\r\n\r\nno mention here\r\n"
	mb := newFakeMailbox(t, 7, mailItem{uid: 9, raw: raw})
	rec := newRecorder()
	ad := mustAdapter(t, mailConfig(mb), rec)
	if err := ad.pollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rec.count() != 0 {
		t.Fatalf("group mail without mention dispatched %d turns", rec.count())
	}
	if _, _, _, stores := mb.snapshot(); len(stores) != 0 {
		t.Fatalf("rejected mail was marked seen: %v", stores)
	}
}

// TestF7GAPGW08EmailIMAP covers the GAP-GW-08 wire rules: BODY.PEEK only, one
// LOGIN across polls, IDLE on the same connection, and cursor persistence.
func TestF7GAPGW08EmailIMAP(t *testing.T) {
	raw := "Message-ID: <m1@example.com>\r\nFrom: Alice <alice@example.com>\r\n\r\nbody\r\n"
	mb := newFakeMailbox(t, 7, mailItem{uid: 5, raw: raw})
	cursor := &memCursor{}
	cfg := mailConfig(mb)
	cfg.Cursors = cursor
	cfg.IDLE = 50 * time.Millisecond
	ad := mustAdapter(t, cfg, newRecorder())
	if err := ad.pollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := ad.pollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	cmds, logins, idles, _ := mb.snapshot()
	if logins != 1 {
		t.Fatalf("login count = %d, want one reused connection", logins)
	}
	if idles != 2 {
		t.Fatalf("idle count = %d, want one per poll", idles)
	}
	for _, cmd := range cmds {
		if strings.Contains(cmd, "BODY[]") && !strings.Contains(cmd, "BODY.PEEK[]") {
			t.Fatalf("non-peeking fetch: %q", cmd)
		}
	}
	if got := cursor.get(); got.UIDValidity != 7 || got.UID != 5 {
		t.Fatalf("cursor = %+v, want {7 5}", got)
	}
}

// TestF7GAPGW08EmailCursor proves the UIDVALIDITY generation reset and the
// persisted-restart path.
func TestF7GAPGW08EmailCursor(t *testing.T) {
	raw := "Message-ID: <m2@example.com>\r\nFrom: Alice <alice@example.com>\r\n\r\nbody\r\n"
	mb := newFakeMailbox(t, 8, mailItem{uid: 3, raw: raw})
	cursor := &memCursor{c: Cursor{UIDValidity: 7, UID: 42}, found: true}
	cfg := mailConfig(mb)
	cfg.Cursors = cursor
	ad := mustAdapter(t, cfg, newRecorder())
	if err := ad.pollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	cmds, _, _, _ := mb.snapshot()
	if len(cmds) == 0 || !containsCommand(cmds, "UID FETCH 1:*") {
		t.Fatalf("commands = %v, want a reset window", cmds)
	}
	if got := cursor.get(); got.UIDValidity != 8 || got.UID != 3 {
		t.Fatalf("cursor = %+v, want {8 3}", got)
	}
}

// TestF7EmailCursorPersist proves a restarted adapter resumes at the persisted
// UID instead of refetching the mailbox.
func TestF7EmailCursorPersist(t *testing.T) {
	mb := newFakeMailbox(t, 8)
	cursor := &memCursor{c: Cursor{UIDValidity: 8, UID: 12}, found: true}
	cfg := mailConfig(mb)
	cfg.Cursors = cursor
	ad := mustAdapter(t, cfg, newRecorder())
	if ad.lastUID != 12 || ad.cursor.UIDValidity != 8 {
		t.Fatalf("resumed cursor = %+v uid %d", ad.cursor, ad.lastUID)
	}
	empty := &memCursor{}
	cfg.Cursors = empty
	fresh := mustAdapter(t, cfg, newRecorder())
	if fresh.lastUID != 0 {
		t.Fatalf("empty store resumed at uid %d", fresh.lastUID)
	}
}

func containsCommand(cmds []string, want string) bool {
	for _, cmd := range cmds {
		if strings.Contains(cmd, want) {
			return true
		}
	}
	return false
}
