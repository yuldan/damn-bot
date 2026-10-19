package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFileSpillRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewFileSpill(dir)
	data := []byte(strings.Repeat("blob", 256*1024)) // 1 MiB
	ref, err := s.Put(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	want := shaHex(data)
	if ref.SHA != want || ref.Size != int64(len(data)) {
		t.Fatalf("ref = %+v, want sha %s size %d", ref, want, len(data))
	}
	wantPath := filepath.Join(dir, "blobs", want[:2], want)
	if ref.Path != wantPath {
		t.Fatalf("path = %s, want %s", ref.Path, wantPath)
	}
	got, err := s.Get(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("round-trip mismatch")
	}
	// Idempotent: a second Put leaves one file.
	again, err := s.Put(context.Background(), data)
	if err != nil || again.SHA != want {
		t.Fatalf("second Put: %+v %v", again, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "blobs", "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("blob files = %d, want 1", len(matches))
	}
}

func TestFileSpillErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewFileSpill(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, []byte("x")); err == nil {
		t.Fatal("cancelled Put succeeded")
	}
	if _, err := s.Get(ctx, strings.Repeat("0", 64)); err == nil {
		t.Fatal("cancelled Get succeeded")
	}
	if _, err := s.Get(context.Background(), "short"); err == nil {
		t.Fatal("invalid sha accepted")
	}
	if _, err := s.Get(context.Background(), strings.Repeat("a", 64)); codeOf(err) != CodeBlobMissing {
		t.Fatalf("missing blob code = %s (%v)", codeOf(err), err)
	}
	// A corrupted file fails its content hash.
	ref, err := s.Put(context.Background(), []byte("good"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(ref.Path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("evil"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), ref.SHA); codeOf(err) != CodeInternal {
		t.Fatalf("corrupt blob code = %s", codeOf(err))
	}
}

func TestSpillOversizedResult(t *testing.T) {
	t.Parallel()
	big := repeated(10000)
	tool := newContentTool("big", big)
	d, _, sp := func() (*Dispatcher, *journalRec, *memSpill) {
		j := &journalRec{}
		sp := newMemSpill()
		d, err := NewDispatcher(&Config{Registry: mustReg(t, tool), Journal: j, Spill: sp, Redactor: testRedactor(), Mode: ModeAuto})
		if err != nil {
			t.Fatal(err)
		}
		return d, j, sp
	}()
	out := d.Dispatch(context.Background(), callOf("big", `{"body":"x"}`))
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if out.Spilled == nil {
		t.Fatal("large result was not spilled")
	}
	if !strings.Contains(out.Content, "[spill:") {
		t.Fatalf("preview = %q", out.Content)
	}
	body, _, _ := strings.Cut(out.Content, "\n[spill:")
	if n := utf8.RuneCountInString(body); n > SpillPreview {
		t.Fatalf("preview has %d runes, want ≤ %d", n, SpillPreview)
	}
	if got := out.Meta["content_blob_sha"]; got != out.Spilled.SHA {
		t.Fatalf("meta sha = %v, want %s", got, out.Spilled.SHA)
	}
	got, err := sp.Get(context.Background(), out.Spilled.SHA)
	if err != nil || string(got) != big {
		t.Fatalf("spill payload mismatch: %v", err)
	}
}

func TestSpillShortPreviewAfterAggregate(t *testing.T) {
	t.Parallel()
	tool := newContentTool("gen", repeated(4000))
	j := &journalRec{}
	d, err := NewDispatcher(&Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if out := d.Dispatch(context.Background(), Call{Name: "gen", Args: []byte(`{"body":"x"}`)}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	out := d.Dispatch(context.Background(), Call{Name: "gen", Args: []byte(`{"body":"x"}`)})
	if out.Spilled == nil {
		t.Fatal("result after the aggregate was not spilled")
	}
	body, _, _ := strings.Cut(out.Content, "\n[spill:")
	if n := utf8.RuneCountInString(body); n > ShortPreview {
		t.Fatalf("preview has %d runes, want ≤ %d", n, ShortPreview)
	}
}

func TestPagedToolCapDropsAfterAggregate(t *testing.T) {
	t.Parallel()
	tool := &stubTool{
		name: "read_file", schema: echoSchema("read_file"), kind: KindRead, effect: EffectRead,
		run: func(_ context.Context, env Env, _ json.RawMessage) (Result, error) {
			return Result{OK: true, Content: repeated(env.PageChars)}, nil
		},
	}
	j := &journalRec{}
	d, err := NewDispatcher(&Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto, WorkspaceTrusted: true})
	if err != nil {
		t.Fatal(err)
	}
	first := d.Dispatch(context.Background(), Call{Name: "read_file", Args: []byte(`{"body":"x"}`)})
	if first.Err != nil || len(first.Content) != PageChars {
		t.Fatalf("first page len = %d pagechars=%d (%v) tail=%q", len(first.Content), tool.last.PageChars, first.Err, first.Content[len(first.Content)-50:])
	}
	second := d.Dispatch(context.Background(), Call{Name: "read_file", Args: []byte(`{"body":"x"}`)})
	if second.Err != nil || len(second.Content) != PageChars {
		t.Fatalf("second page len = %d", len(second.Content))
	}
	third := d.Dispatch(context.Background(), Call{Name: "read_file", Args: []byte(`{"body":"x"}`)})
	if third.Err != nil || len(third.Content) != ReducedPage {
		t.Fatalf("third page len = %d, want %d (ADR-031)", len(third.Content), ReducedPage)
	}
	if third.Spilled != nil {
		t.Fatal("paged tool must not spill")
	}
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
