package gwplat_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gateway/render"
	"github.com/damnbot/damnbot/internal/gwplat"
)

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("secret not found")
	}
	return value, nil
}

// TestPlatformAdapterContract is the shared F7 adapter boundary (ADR-326):
// one subtest per adapter proves the closed descriptor, explicit config+secret
// activation, SEC-10 identity strength, mention policy, replay dedupe, and the
// outq/render outbound seam. Adapter-specific wire tests live next to each
// adapter.
func TestPlatformAdapterContract(t *testing.T) {
	names := []string{"buzz", "discord", "email", "irc", "matrix", "msgraph_webhook", "ntfy", "photon", "raft", "signal", "simplex", "webhook", "weixin", "whatsapp_cloud", "whatsapp_web"}
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if reg.Len() != len(names) {
		t.Fatalf("registry = %d descriptors, want %d", reg.Len(), len(names))
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			d, err := reg.Lookup(name)
			if err != nil {
				t.Fatal(err)
			}
			contractDescriptor(t, &d)
			contractActivation(t, &d)
			contractPolicy(t, &d)
			contractOutbox(t, &d)
		})
	}

	t.Run("closed_registry", func(t *testing.T) {
		d, err := reg.Lookup("buzz")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Lookup("not-a-platform"); !errors.Is(err, gwplat.ErrUnknown) {
			t.Fatalf("unknown lookup = %v, want ErrUnknown", err)
		}
		if err := reg.Register(&d); !errors.Is(err, gwplat.ErrDuplicate) {
			t.Fatalf("duplicate register = %v, want ErrDuplicate", err)
		}
		alien := d
		alien.Name = "not-a-platform"
		if err := gwplat.NewRegistry().Register(&alien); !errors.Is(err, gwplat.ErrDescriptor) {
			t.Fatalf("alien platform = %v, want ErrDescriptor", err)
		}
		alwaysOn := d
		alwaysOn.DefaultOff = false
		if err := gwplat.NewRegistry().Register(&alwaysOn); !errors.Is(err, gwplat.ErrDescriptor) {
			t.Fatalf("default-on descriptor = %v, want ErrDescriptor", err)
		}
		weakened := d
		weakened.Caps.MaxLen++
		if err := gwplat.NewRegistry().Register(&weakened); !errors.Is(err, gwplat.ErrDescriptor) {
			t.Fatalf("caps divergence = %v, want ErrDescriptor", err)
		}
	})
}

func contractDescriptor(t *testing.T, d *gwplat.Descriptor) {
	t.Helper()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if !d.DefaultOff {
		t.Fatal("adapter is not default-off")
	}
	want, ok := gateway.CapsFor(d.Name)
	if !ok {
		t.Fatalf("%s missing from the SEC-10 table", d.Name)
	}
	if d.Name == "irc" {
		want = gateway.IRCCaps(true)
	}
	if d.Caps != want {
		t.Fatalf("%s caps = %+v, want %+v", d.Name, d.Caps, want)
	}
	if d.Caps.MaxChunksPerReply <= 0 || d.Caps.MaxLen <= 0 {
		t.Fatalf("%s has no send budget", d.Name)
	}
	if d.Render == "" {
		t.Fatalf("%s has no renderer", d.Name)
	}
}

func contractActivation(t *testing.T, d *gwplat.Descriptor) {
	t.Helper()
	ctx := t.Context()
	disabled := gwplat.RequireEnabled(d.Name, false)
	if !errors.Is(disabled, gwplat.ErrUnavailable) || !strings.Contains(disabled.Error(), d.Name+"_disabled") {
		t.Fatalf("disabled adapter = %v, want module_unavailable{%s_disabled}", disabled, d.Name)
	}
	if enabled := gwplat.RequireEnabled(d.Name, true); enabled != nil {
		t.Fatalf("enabled adapter refused: %v", enabled)
	}
	if _, missing := gwplat.ResolveSecrets(ctx, d, nil); len(d.Secrets) > 0 && !errors.Is(missing, gwplat.ErrMissingSecret) {
		t.Fatalf("nil secret source = %v, want ErrMissingSecret", missing)
	}
	if len(d.Secrets) == 0 {
		return
	}
	src := secrets{}
	for _, name := range d.Secrets {
		src[name] = "s3cr3t-value"
	}
	got, err := gwplat.ResolveSecrets(ctx, d, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range d.Secrets {
		if got[name] != "s3cr3t-value" {
			t.Fatalf("secret %q not loaded", name)
		}
	}
	src[d.Secrets[0]] = ""
	_, blankErr := gwplat.ResolveSecrets(ctx, d, src)
	if !errors.Is(blankErr, gwplat.ErrMissingSecret) {
		t.Fatalf("blank secret = %v, want ErrMissingSecret", blankErr)
	}
	if strings.Contains(blankErr.Error(), "s3cr3t-value") {
		t.Fatal("secret value leaked into an activation error")
	}
	if strings.Contains(blankErr.Error(), d.Secrets[0]) {
		t.Fatalf("activation error named the secret: %v", blankErr)
	}
}

func contractPolicy(t *testing.T, d *gwplat.Descriptor) {
	t.Helper()
	var handled []string
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled = append(handled, in.UpdateID)
		return nil
	})
	group := &gateway.Inbound{Platform: d.Name, ChatID: "room", SenderID: "alice", UpdateID: "g1", Group: true}
	if err := gwplat.Dispatch(t.Context(), h, group); err != nil {
		t.Fatal(err)
	}
	if len(handled) != 0 {
		t.Fatalf("group message without mention dispatched %v", handled)
	}
	mentioned := *group
	mentioned.UpdateID, mentioned.Mentioned = "g2", true
	if err := gwplat.Dispatch(t.Context(), h, &mentioned); err != nil {
		t.Fatal(err)
	}
	dm := &gateway.Inbound{Platform: d.Name, ChatID: "a", SenderID: "alice", UpdateID: "d1"}
	if err := gwplat.Dispatch(t.Context(), h, dm); err != nil {
		t.Fatal(err)
	}
	if want := []string{"g2", "d1"}; strings.Join(handled, ",") != strings.Join(want, ",") {
		t.Fatalf("dispatched %v, want %v", handled, want)
	}
	if err := gwplat.Dispatch(t.Context(), nil, dm); !errors.Is(err, gwplat.ErrNoInbound) {
		t.Fatalf("nil handler = %v, want ErrNoInbound", err)
	}

	exerciseBackoff(t)
	window := gwplat.NewDedupe(4, time.Minute)
	now := time.Unix(1_700_000_000, 0)
	if !window.Claim("id-1", now) || window.Claim("id-1", now.Add(time.Second)) {
		t.Fatal("replayed id was not deduped")
	}
	if !window.Claim("id-2", now) {
		t.Fatal("distinct id was deduped")
	}
	if window.Len() != 2 {
		t.Fatalf("dedupe window = %d, want 2", window.Len())
	}
	for i := range 10 {
		window.Claim("id-"+strings.Repeat("x", i+1), now)
	}
	if window.Len() > 4 {
		t.Fatalf("dedupe window grew to %d, cap 4", window.Len())
	}

	if err := gwplat.Pairable(d); err != nil {
		t.Fatalf("strong identity refused: %v", err)
	}
	weak := *d
	weak.Caps.IdentityStrength = gateway.StrengthWeak
	if err := gwplat.Pairable(&weak); !errors.Is(err, gwplat.ErrWeakIdentity) {
		t.Fatalf("weak descriptor pairable: %v", err)
	}
}

// exerciseBackoff keeps the ctx-aware reconnect wait on the tested path.
func exerciseBackoff(t *testing.T) {
	t.Helper()
	if err := (gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond}).Wait(t.Context(), 1); err != nil {
		t.Fatalf("backoff wait: %v", err)
	}
}

func contractOutbox(t *testing.T, d *gwplat.Descriptor) {
	t.Helper()
	if _, err := gwplat.NewOutbox(d, nil); !errors.Is(err, gwplat.ErrNoOutbox) {
		t.Fatalf("outbox without scheduler = %v, want ErrNoOutbox", err)
	}
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	out, err := gwplat.NewOutbox(d, sched)
	if err != nil {
		t.Fatal(err)
	}
	if out.MaxLen() != d.Caps.MaxLen {
		t.Fatalf("outbox max len = %d, want %d", out.MaxLen(), d.Caps.MaxLen)
	}
	sent := make(chan string, 16)
	err = out.Reply(ctx, gwplat.Reply{
		Chat:     "chat-1",
		Markdown: "hello **world**",
		Send: func(_ context.Context, text string) error {
			sent <- text
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-sent:
		if !strings.Contains(text, "world") {
			t.Fatalf("outbox lost the reply body: %q", text)
		}
		if d.Render == render.PlatformPlain && strings.Contains(text, "**") {
			t.Fatalf("plain renderer leaked markup: %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("outbox never sent the reply")
	}
	if err := out.Reply(ctx, gwplat.Reply{Chat: "chat-1"}); !errors.Is(err, gwplat.ErrNoSender) {
		t.Fatalf("reply without sender = %v, want ErrNoSender", err)
	}
	if err := out.Reply(ctx, gwplat.Reply{Send: func(context.Context, string) error { return nil }}); !errors.Is(err, gwplat.ErrNoReply) {
		t.Fatalf("reply without chat = %v, want ErrNoReply", err)
	}
}
