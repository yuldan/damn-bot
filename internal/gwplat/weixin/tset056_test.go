package weixin

import (
	"bytes"
	"errors"
	"runtime"
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/media/silk"
	"github.com/damnbot/damnbot/internal/toolset"
)

// TestF7TSET056WeixinBundle closes TSET-056: `hermes-weixin` is a composite
// with no tool registry of its own, it activates only while the Weixin adapter
// is active, it carries no shell/write/outbound tool, and SILK decoding is
// available through internal/media/silk plus the Config.SilkDecoder seam the
// adapter uses in production (not through an adapter poll).
func TestF7TSET056WeixinBundle(t *testing.T) {
	t.Parallel()
	bundle := checkWeixinBundle(t)
	checkWeixinActivation(t, bundle)
	checkWeixinSilkSeam(t)
}

// checkWeixinBundle proves the closed bundle row and its composition.
func checkWeixinBundle(t *testing.T) gwplat.Bundle {
	t.Helper()
	rows := gwplat.BundlesFor("weixin")
	if len(rows) != 1 || rows[0].Toolset != "hermes-weixin" {
		t.Fatalf("weixin rows = %+v, want one hermes-weixin row", rows)
	}
	def, ok := weixinBundleDef("hermes-weixin")
	if !ok {
		t.Fatal("hermes-weixin is not a built-in toolset")
	}
	if len(def.Tools) != 0 {
		t.Fatalf("hermes-weixin owns a tool registry: %v", def.Tools)
	}
	want := []string{"safe", "clarify", "memory", "session_search"}
	if !slices.Equal(def.Includes, want) {
		t.Fatalf("includes = %v, want %v", def.Includes, want)
	}
	for _, inc := range def.Includes {
		if _, ok := weixinBundleDef(inc); !ok {
			t.Errorf("hermes-weixin includes unknown toolset %q", inc)
		}
	}
	return rows[0]
}

func weixinBundleDef(name string) (toolset.Definition, bool) {
	for _, d := range toolset.Builtin() {
		if d.Name == name {
			return d, true
		}
	}
	return toolset.Definition{}, false
}

// checkWeixinActivation is the negative control: an inactive adapter exposes
// zero tools, and the active bundle exposes only the safe chat set.
func checkWeixinActivation(t *testing.T, bundle gwplat.Bundle) {
	t.Helper()
	sel, err := bundle.Selection(runtime.GOOS, true, toolset.DeclaredTools())
	if err != nil {
		t.Fatalf("active selection: %v", err)
	}
	if len(sel.Tools) == 0 || len(sel.Unavailable) != 0 {
		t.Fatalf("active selection = %+v", sel)
	}
	for _, banned := range []string{"shell", "write_file", "edit_file", "room_send", "send_message"} {
		if sel.Allowed[banned] {
			t.Errorf("hermes-weixin exposes %q", banned)
		}
	}
	off, err := bundle.Selection(runtime.GOOS, false, toolset.DeclaredTools())
	if err != nil {
		t.Fatalf("inactive selection: %v", err)
	}
	if len(off.Tools) != 0 || len(off.Allowed) != 0 {
		t.Fatalf("inactive adapter exposes %d tools", len(off.Tools))
	}
	if _, ok := gwplat.ActiveToolset("weixin", nil); ok {
		t.Error("weixin activates a toolset while the adapter is inactive")
	}
	if name, ok := gwplat.ActiveToolset("weixin", map[string]bool{"weixin": true}); !ok || name != "hermes-weixin" {
		t.Errorf("active toolset = %q (ok=%v), want hermes-weixin", name, ok)
	}
}

// checkWeixinSilkSeam proves MEDIA-031 is reachable for this bundle: the
// decoder package decodes, and Config.SilkDecoder+Config.STT compose the same
// VoiceToText the adapter builds in prepare, failing closed without a decoder.
func checkWeixinSilkSeam(t *testing.T) {
	t.Helper()
	decoder := decoderBinary(t)
	var pcm bytes.Buffer
	if err := silk.Decode(t.Context(), []byte("silk"), &pcm, &silk.Config{Argv: []string{decoder}}); err != nil {
		t.Fatalf("silk.Decode: %v", err)
	}
	if pcm.Len() == 0 {
		t.Fatal("silk.Decode produced no PCM16")
	}
	stt := &fakeSTT{}
	cfg := Config{SilkDecoder: decoder, STT: stt}
	prepared := prepare(&cfg)
	if prepared.VoiceToText == nil {
		t.Fatal("Config.SilkDecoder + Config.STT did not compose VoiceToText")
	}
	text, err := prepared.VoiceToText(t.Context(), []byte("silk"))
	if err != nil {
		t.Fatalf("composed voice: %v", err)
	}
	if text != "stt:01000200" || stt.calls != 1 {
		t.Fatalf("composed voice = %q (stt calls %d), want stt:01000200", text, stt.calls)
	}
	if _, err := composeVoice("", stt)(t.Context(), []byte("silk")); !errors.Is(err, silk.ErrDecoderMissing) {
		t.Fatalf("missing decoder err = %v, want ErrDecoderMissing", err)
	}
}
