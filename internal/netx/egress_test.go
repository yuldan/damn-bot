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
var clientCall = regexp.MustCompile(`\.(?:WebSocket)?Client\("([^"]+)",\s*(?:netx\.)?(Inference|Tool|Background|Gateway)\)`)

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
	for _, problem := range registryProblems(Registry(), scanClientCalls(t)) {
		t.Error(problem)
	}
}

// registryProblems reports every production client missing from the registry,
// registered with another purpose, or left as a stale/placeholder entry. The
// gate and its negative control share this one rule.
func registryProblems(reg map[string]Egress, seen map[string]Purpose) []string {
	var problems []string
	for _, name := range slices.Sorted(maps.Keys(seen)) {
		e, ok := reg[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("egress %q has a production call site but no registry entry", name))
			continue
		}
		if e.Purpose != seen[name] {
			problems = append(problems, fmt.Sprintf("egress %q registry purpose = %s, call site = %s", name, e.Purpose, seen[name]))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(reg)) {
		e := reg[name]
		if _, ok := seen[name]; !ok {
			problems = append(problems, fmt.Sprintf("registry entry %q has no production call site", name))
		}
		if e.Host == "" || e.Trigger == "" {
			problems = append(problems, fmt.Sprintf("registry entry %q is missing host or trigger: %+v", name, e))
		}
	}
	return problems
}

// TestEgressRegistryRejectsUnregistered is the negative control for
// [TestEgressRegistryComplete]: a label used by production code but absent
// from the registry is an error, not a silent pass.
func TestEgressRegistryRejectsUnregistered(t *testing.T) {
	t.Parallel()
	reg := map[string]Egress{"known": {Purpose: Tool, Host: "h", Trigger: "t"}}
	seen := map[string]Purpose{"known": Tool, "ghost": Gateway}
	problems := registryProblems(reg, seen)
	if !slices.Contains(problems, `egress "ghost" has a production call site but no registry entry`) {
		t.Fatalf("unregistered label passed the gate: %q", problems)
	}
}

// TestPeerDMEgress (GAP-OPS-02, GW-013): the peer DM send path is user
// initiated gateway egress, so its registry entry carries the same purpose
// the production call site declares and real, non-placeholder metadata.
func TestPeerDMEgress(t *testing.T) {
	t.Parallel()
	e, ok := Registry()["peer-dm"]
	if !ok {
		t.Fatal(`registry has no "peer-dm" entry`)
	}
	if e.Purpose != Gateway {
		t.Fatalf("peer-dm purpose = %s, want gateway", e.Purpose)
	}
	if got := scanClientCalls(t)["peer-dm"]; got != Gateway {
		t.Fatalf("peer-dm call site purpose = %s, want gateway", got)
	}
	if e.Host == "" || e.Trigger == "" {
		t.Fatalf("peer-dm entry is a placeholder: %+v", e)
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
			if minimalBuild && strings.HasPrefix(string(data), "//go:build !minimal") {
				return
			}
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
