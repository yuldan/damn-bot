package main

import (
	"bytes"
	"context"
	"errors"
	stdimage "image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/media/preflight"
)

func writePNG(t *testing.T, path string, w, h int) []byte {
	t.Helper()
	img := stdimage.NewNRGBA(stdimage.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{R: uint8(x % 256), G: uint8(y % 256), B: uint8((x + y) % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestF6SLASH099Image: /image reads only inside the workspace and the bytes go
// through the same preflight the provider path uses.
func TestF6SLASH099Image(t *testing.T) {
	t.Parallel()
	root, outside := t.TempDir(), t.TempDir()
	inside := filepath.Join(root, "shots", "big.png")
	if err := os.MkdirAll(filepath.Dir(inside), 0o750); err != nil {
		t.Fatal(err)
	}
	data := writePNG(t, inside, 3000, 2000)
	outsidePath := filepath.Join(outside, "secret.png")
	writePNG(t, outsidePath, 4, 4)

	got, rerr := readWorkspaceImage(root, filepath.Join("shots", "big.png"))
	if rerr != nil || !bytes.Equal(got, data) {
		t.Fatalf("read inside = %d bytes, %v", len(got), rerr)
	}
	if _, err := readWorkspaceImage(root, "../outside.png"); err == nil {
		t.Fatal("parent path was allowed")
	}
	if _, err := readWorkspaceImage(root, outsidePath); err == nil {
		t.Fatal("absolute path outside the workspace was allowed")
	}
	if _, err := readWorkspaceImage(root, "shots"); err == nil {
		t.Fatal("directory was allowed")
	}

	res, err := preflight.Preflight(data, preflight.Default())
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !res.Resized || res.Width > preflight.Default().MaxSide || res.Height > preflight.Default().MaxSide {
		t.Fatalf("preflight did not bound 3000x2000: %+v", res)
	}
	text := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(text, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, rerr := readWorkspaceImage(root, "notes.txt")
	if rerr != nil {
		t.Fatal(rerr)
	}
	if _, err := preflight.Preflight(raw, preflight.Default()); err == nil {
		t.Fatal("preflight accepted a text file as an image")
	}
}

// TestClipboardImageReaders: missing utilities and an unreadable clipboard are
// clear notices; the first successful reader wins.
func TestClipboardImageReaders(t *testing.T) {
	t.Parallel()
	lookErr := func(string) (string, error) { return "", errors.New("not found") }
	if _, err := readClipboardImage(context.Background(), "linux", lookErr, nil); !errors.Is(err, errNoClipboardImage) {
		t.Fatalf("no utilities = %v", err)
	}
	look := func(name string) (string, error) { return "/usr/bin/" + name, nil }
	runErr := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no image") }
	if _, err := readClipboardImage(context.Background(), "linux", look, runErr); !errors.Is(err, errNoClipboardImage) {
		t.Fatalf("unreadable clipboard = %v", err)
	}
	want := []byte("png")
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "wl-paste" {
			t.Fatalf("first reader = %s", name)
		}
		return want, nil
	}
	if _, err := readClipboardImage(context.Background(), "darwin", lookErr, run); !errors.Is(err, errNoClipboardImage) {
		t.Fatalf("darwin without pngpaste = %v", err)
	}
	got, err := readClipboardImage(context.Background(), "linux", look, run)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read = %q, %v", got, err)
	}
}

// TestF6SLASH099ImageReachesPrompt: a spilled image's sha resolves back to
// bytes on the user message the provider will see; a missing blob fails
// closed instead of sending a prompt without the attachment.
func TestF6SLASH099ImageReachesPrompt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	pb, err := db.PutBlob([]byte("png"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error { return tx.Publish(pb) }); err != nil {
		t.Fatal(err)
	}
	a := &assemblerAdapter{db: db}
	hist := []compaction.Message{{Role: "user", Content: "look", Images: []string{pb.Sha}}}
	out := []loop.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "look"}}
	if err := a.attachImages(ctx, hist, 1, out); err != nil {
		t.Fatalf("attachImages: %v", err)
	}
	if len(out[1].Attachments) != 1 || out[1].Attachments[0].Kind != "image" ||
		string(out[1].Attachments[0].Data) != "png" {
		t.Fatalf("attachments = %+v", out[1].Attachments)
	}
	if len(out[0].Attachments) != 0 {
		t.Fatal("image landed on the system message")
	}
	missing := []compaction.Message{{Role: "user", Images: []string{strings.Repeat("0", 64)}}}
	if err := a.attachImages(ctx, missing, 1, out); err == nil {
		t.Fatal("missing blob was ignored")
	}
}
