//go:build unix

package code

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
)

// realBackend delegates to the local runner but claims the network
// capability, so the interpreter smoke test exercises the real argv path.
// Network deny itself is tested negatively in code_test.go; production
// local has Caps.Network=false and is refused.
type realBackend struct{ l *exec.Local }

func (r realBackend) ID() string      { return "local-harness" }
func (r realBackend) Caps() exec.Caps { return exec.Caps{Signals: true, Network: true, SharesFS: true} }

func (r realBackend) Run(ctx context.Context, s *exec.Spec) (exec.Result, error) {
	res, err := r.l.Run(ctx, s)
	if err != nil {
		return res, fmt.Errorf("test local run: %w", err)
	}
	return res, nil
}

func (r realBackend) Sweep(ctx context.Context, id string) (int, error) {
	n, err := r.l.Sweep(ctx, id)
	if err != nil {
		return n, fmt.Errorf("test local sweep: %w", err)
	}
	return n, nil
}

func (r realBackend) Close(ctx context.Context) error {
	if err := r.l.Close(ctx); err != nil {
		return fmt.Errorf("test local close: %w", err)
	}
	return nil
}

func TestRealInterpreterSmoke(t *testing.T) {
	cases := []struct {
		lang Language
		bin  string
		code string
		want string
	}{
		{Python, "python3", "import json,sys\nprint(json.load(sys.stdin)['n']+1)", "42"},
		{JavaScript, "node", "let s='';process.stdin.on('data',d=>s+=d).on('end',()=>console.log(JSON.parse(s).n+1))", "42"},
		{Go, "go", "package main\nimport (\"encoding/json\";\"os\")\nfunc main(){var v struct{N int};json.NewDecoder(os.Stdin).Decode(&v);println(v.N+1)}", "42"},
	}
	for _, tc := range cases {
		t.Run(string(tc.lang), func(t *testing.T) {
			if _, err := osexec.LookPath(tc.bin); err != nil {
				t.Skipf("%s absent", tc.bin)
			}
			l := exec.NewLocal(exec.New(t.Context()))
			t.Cleanup(func() {
				//nolint:errcheck,gosec // test cleanup
				l.Close(context.Background())
			})
			r, err := New(Config{Backend: realBackend{l: l}, Env: os.Environ()})
			if err != nil {
				t.Fatal(err)
			}
			res, err := r.Run(t.Context(), &Request{Language: tc.lang, Code: tc.code, Stdin: []byte(`{"n":41}`)})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !strings.Contains(string(res.Stdout), tc.want) {
				t.Fatalf("stdout = %q", res.Stdout)
			}
		})
	}
}
