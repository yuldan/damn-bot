package netx

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// clientCall matches a production egress call site with a literal name and
// purpose, e.g. net.Client("refs", netx.Tool).
var clientCall = regexp.MustCompile(`\.Client\("([^"]+)",\s*(?:netx\.)?(Inference|Tool|Background|Gateway)\)`)

func purposeOf(name string) Purpose {
	switch name {
	case "Inference":
		return Inference
	case "Tool":
		return Tool
	case "Gateway":
		return Gateway
	default:
		return Background
	}
}

// TestEgressRegistryComplete (GAP-OPS-02): every production egress client is
// registered with the same purpose, every registry entry has a live call
// site, and no entry is a placeholder.
func TestEgressRegistryComplete(t *testing.T) {
	t.Parallel()
	reg := Registry()
	seen := scanClientCalls(t)
	for _, name := range slices.Sorted(maps.Keys(seen)) {
		e, ok := reg[name]
		if !ok {
			t.Errorf("egress %q has a production call site but no registry entry", name)
			continue
		}
		if e.Purpose != seen[name] {
			t.Errorf("egress %q registry purpose = %s, call site = %s", name, e.Purpose, seen[name])
		}
	}
	for name, e := range reg {
		if _, ok := seen[name]; !ok {
			t.Errorf("registry entry %q has no production call site", name)
		}
		if e.Host == "" || e.Trigger == "" {
			t.Errorf("registry entry %q is missing host or trigger: %+v", name, e)
		}
	}
}

// scanClientCalls walks the production Go sources and records every literal
// egress client as name → purpose.
func scanClientCalls(t *testing.T) map[string]Purpose {
	t.Helper()
	seen := map[string]Purpose{}
	for _, dir := range []string{"cmd", "internal"} {
		base := filepath.Join("..", "..", dir)
		walkSources(t, base, func(path string, data []byte) {
			for _, m := range clientCall.FindAllStringSubmatch(string(data), -1) {
				p := purposeOf(m[2])
				if old, ok := seen[m[1]]; ok && old != p {
					t.Errorf("%s: egress %q is registered as %s elsewhere", path, m[1], p)
				}
				seen[m[1]] = p
			}
		})
	}
	return seen
}

// walkSources calls fn for every non-test Go file below base.
func walkSources(t *testing.T, base string, fn func(string, []byte)) {
	t.Helper()
	root := os.DirFS(base)
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := fs.ReadFile(root, path)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		fn(filepath.Join(base, path), data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
}

// TestBackgroundOffDoesNotOverBlock (GAP-OPS-02): with background=false only
// Background is refused; inference and tool egress still reach the server.
// This is the negative control for TestEgressDisabled.
func TestBackgroundOffDoesNotOverBlock(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	})
	n := newNet(t, Config{NoBackground: true})
	for _, p := range []Purpose{Inference, Tool, Gateway} {
		resp, err := n.Client("allowed", p).DoOK(get(t, srv.URL))
		if err != nil {
			t.Fatalf("purpose %s refused under background=false: %v", p, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 3 {
		t.Errorf("server hits = %d, want 3", hits.Load())
	}
	resp, err := n.Client("update", Background).Do(get(t, srv.URL))
	closeBody(t, resp)
	if !errors.Is(err, ErrEgressDisabled) {
		t.Fatalf("background error = %v, want ErrEgressDisabled", err)
	}
	if hits.Load() != 3 {
		t.Errorf("server hits after a refused background call = %d, want 3", hits.Load())
	}
}

// TestGatewayPurpose (ADR-247): gateway egress ignores network.background, is
// not DLP-scanned (the Bot API token lives in the URL path), and offline
// refuses it outright even for a loopback target.
func TestGatewayPurpose(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) })
	// Assembled at run time so the repository secret scan never sees a token.
	token := "1234567" + "8:" + strings.Repeat("Q", 35)
	n := newNet(t, Config{NoBackground: true, Secrets: []string{token}})
	resp, err := n.Client("telegram", Gateway).DoOK(get(t, srv.URL+"/bot"+token+"/getMe"))
	if err != nil {
		t.Fatalf("gateway egress was scanned or refused: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("gateway hits = %d, want 1", hits.Load())
	}
	off := newNet(t, Config{Offline: true})
	offResp, offErr := off.Client("telegram", Gateway).Do(get(t, srv.URL))
	closeBody(t, offResp)
	if !errors.Is(offErr, ErrOffline) || retry.ClassifyErr(offErr) != retry.OfflineMode {
		t.Fatalf("offline gateway: %v", offErr)
	}
	if hits.Load() != 1 {
		t.Errorf("offline gateway reached the server: %d hits", hits.Load())
	}
}
