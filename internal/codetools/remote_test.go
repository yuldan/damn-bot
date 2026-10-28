package codetools

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeClient is an editor buffer: every path the client "has open" lives in
// files, writes update it and never touch disk.
type fakeClient struct {
	mu             sync.Mutex
	read, write    bool
	files, written map[string]string
}

func (c *fakeClient) Supports(write bool) bool {
	if write {
		return c.read && c.write
	}
	return c.read
}

func (c *fakeClient) ReadTextFile(_ context.Context, path string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.files[path]
	if !ok {
		return nil, true, nil
	}
	return []byte(data), false, nil
}

func (c *fakeClient) WriteTextFile(_ context.Context, path string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[path] = string(data)
	c.written[path] = string(data)
	return nil
}

func newClientEnv(t *testing.T, v *env, client ClientFS, fallback func(context.Context, bool) error) {
	t.Helper()
	v.e.Close()
	e, err := New(&Config{Sandbox: v.sb, Roots: []string{v.root}, Home: v.home, ClientFS: client, OnClientFallback: fallback})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	v.e = e
}

func TestClientFSReadUsesBuffer(t *testing.T) {
	v := newEnv(t)
	path := filepath.Join(v.root, "f.txt")
	writeFile(t, path, "disk content\n")
	buffer := "unsaved buffer\n"
	c := &fakeClient{read: true, write: true, files: map[string]string{path: buffer}, written: map[string]string{}}
	newClientEnv(t, v, c, nil)

	res, err := v.e.Read(t.Context(), ReadArgs{Path: "f.txt"}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "unsaved buffer") || strings.Contains(res.Content, "disk content") {
		t.Fatalf("read did not use the buffer:\n%s", res.Content)
	}
	want := sumTag(sha256.Sum256([]byte(buffer)))
	tag, ok := res.Meta["tag"].(string)
	if !ok || tag != want {
		t.Fatalf("tag = %q, want %q (sha of the buffer)", tag, want)
	}
}

func TestClientFSEditStaleTag(t *testing.T) {
	v := newEnv(t)
	path := filepath.Join(v.root, "f.txt")
	writeFile(t, path, "disk\n")
	c := &fakeClient{read: true, write: true, files: map[string]string{path: "one\n"}, written: map[string]string{}}
	newClientEnv(t, v, c, nil)

	res, err := v.e.Read(t.Context(), ReadArgs{Path: "f.txt"}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	tag, ok := res.Meta["tag"].(string)
	if !ok {
		t.Fatal("read result has no tag")
	}
	c.mu.Lock()
	c.files[path] = "two\n" // the user typed in the editor after the read
	c.mu.Unlock()
	old, neu := "two", "three"
	_, err = v.e.Edit(t.Context(), EditArgs{Path: "f.txt", Tag: tag, Edits: []EditItem{{OldString: &old, NewString: &neu}}}, Opts{})
	if codeOf(err) != CodeStaleRead || !strings.Contains(err.Error(), "stale_tag") {
		t.Fatalf("edit with a stale tag = %v, want stale_read + stale_tag", err)
	}
	if _, ok := c.written[path]; ok {
		t.Fatal("stale edit wrote the buffer")
	}
	if got := c.files[path]; got != "two\n" {
		t.Fatalf("buffer changed to %q", got)
	}
	got, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil || string(got) != "disk\n" {
		t.Fatalf("disk changed: %q (%v)", got, err)
	}
}

func TestClientFSWriteThroughBuffer(t *testing.T) {
	v := newEnv(t)
	path := filepath.Join(v.root, "f.txt")
	writeFile(t, path, "disk\n")
	c := &fakeClient{read: true, write: true, files: map[string]string{path: "old\n"}, written: map[string]string{}}
	newClientEnv(t, v, c, nil)

	res, err := v.e.Read(t.Context(), ReadArgs{Path: "f.txt"}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	tag, ok := res.Meta["tag"].(string)
	if !ok {
		t.Fatal("read result has no tag")
	}
	if _, werr := v.e.Write(t.Context(), WriteArgs{Path: "f.txt", Content: "new\n", Tag: &tag}, Opts{}); werr != nil {
		t.Fatal(werr)
	}
	if got := c.written[path]; got != "new\n" {
		t.Fatalf("client write = %q", got)
	}
	got, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil || string(got) != "disk\n" {
		t.Fatalf("write touched disk: %q (%v)", got, err)
	}
}

func TestClientFSWriteCreatesBuffer(t *testing.T) {
	v := newEnv(t)
	path := filepath.Join(v.root, "new.txt")
	c := &fakeClient{read: true, write: true, files: map[string]string{}, written: map[string]string{}}
	newClientEnv(t, v, c, nil)
	if _, err := v.e.Write(t.Context(), WriteArgs{Path: "new.txt", Content: "created\n"}, Opts{}); err != nil {
		t.Fatal(err)
	}
	if got := c.written[path]; got != "created\n" {
		t.Fatalf("client create = %q", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("create touched disk: %v", err)
	}
}

func TestClientFSFallbackRecordsAndStaysLocal(t *testing.T) {
	v := newEnv(t)
	writeFile(t, filepath.Join(v.root, "f.txt"), "disk content\n")
	c := &fakeClient{read: false, write: false, files: map[string]string{}, written: map[string]string{}}
	var mu sync.Mutex
	fellBack := map[bool]int{}
	fallback := func(_ context.Context, write bool) error {
		mu.Lock()
		defer mu.Unlock()
		fellBack[write]++
		return nil
	}
	newClientEnv(t, v, c, fallback)

	res, err := v.e.Read(t.Context(), ReadArgs{Path: "f.txt"}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "disk content") {
		t.Fatalf("fallback read = %q", res.Content)
	}
	if _, werr := v.e.Write(t.Context(), WriteArgs{Path: "new.txt", Content: "local\n"}, Opts{}); werr != nil {
		t.Fatal(werr)
	}
	got, err := os.ReadFile(filepath.Join(v.root, "new.txt"))
	if err != nil || string(got) != "local\n" {
		t.Fatalf("fallback write did not hit disk: %q (%v)", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if fellBack[false] != 1 || fellBack[true] != 1 {
		t.Fatalf("fallback calls = %v, want one per capability", fellBack)
	}
}

func TestClientFSFallbackErrorFailsTheCall(t *testing.T) {
	v := newEnv(t)
	writeFile(t, filepath.Join(v.root, "f.txt"), "disk\n")
	c := &fakeClient{read: false, write: false, files: map[string]string{}, written: map[string]string{}}
	newClientEnv(t, v, c, func(context.Context, bool) error { return errors.New("event store down") })
	if _, err := v.e.Read(t.Context(), ReadArgs{Path: "f.txt"}, Opts{}); err == nil || !strings.Contains(err.Error(), "event store down") {
		t.Fatalf("unrecorded fallback = %v, want the callback error", err)
	}
}

func TestClientFSWriteCapabilityNeedsRead(t *testing.T) {
	if (&fakeClient{read: false, write: true}).Supports(true) {
		t.Fatal("write without read must be unsupported: tags come from the buffer")
	}
}
