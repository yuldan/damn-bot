//go:build !minimal

package auth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

func helperProgram(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool is not on PATH")
	}
	path := filepath.Join(t.TempDir(), "keycmd.go")
	src := `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Print("TOKEN-SECRET")
	switch os.Args[1] {
	case "fail":
		fmt.Fprint(os.Stderr, "stderr-"+strings.Repeat("x", 10))
		os.Exit(7)
	case "block":
		select {}
	case "big":
		fmt.Print(strings.Repeat("y", 1000))
	}
}
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func commandSource(t *testing.T, mode string) Source {
	t.Helper()
	src, err := CommandSource([]string{"go", "run", helperProgram(t), mode})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

type spyGetter struct {
	names *[]string
	value string
	err   error
}

func (s spyGetter) Get(_ context.Context, name string) (string, error) {
	*s.names = append(*s.names, name)
	return s.value, s.err
}

// TestCredentialNoImplicitDiscovery (EC-SEC-002): only the configured env
// name is read; a workspace .env and ambient GH_TOKEN/CLAUDE_* values are
// never consulted, and no command runs by itself.
func TestCredentialNoImplicitDiscovery(t *testing.T) {
	g := phase.Current(t)
	if !g.Item(t, "EC-SEC-002", "F5", "covered") {
		return
	}
	t.Run("EC-SEC-002", func(t *testing.T) {
		t.Run("only configured env", testNoImplicitEnv)
		t.Run("dotenv and symlink rejected", testDotenvRejected)
		t.Run("env grammar", testEnvGrammar)
	})
}

func testNoImplicitEnv(t *testing.T) {
	var calls []string
	env := map[string]string{
		"OPENAI_API_KEY":          "ambient-openai",
		"GH_TOKEN":                "ambient-gh",
		"CLAUDE_CODE_OAUTH_TOKEN": "ambient-claude",
	}
	ran := false
	r := &Resolver{
		Getenv: func(name string) string { calls = append(calls, name); return env[name] },
		Run: func(context.Context, []string, int) (string, error) {
			ran = true
			return "", errors.New("must not run")
		},
	}
	src, err := EnvSource("MY_PROVIDER_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), src); !errors.Is(err, ErrCredential) {
		t.Fatalf("missing configured env = %v", err)
	}
	if len(calls) != 1 || calls[0] != "MY_PROVIDER_KEY" {
		t.Fatalf("env lookups = %v, want exactly MY_PROVIDER_KEY", calls)
	}
	if ran {
		t.Fatal("credential command ran without configuration")
	}
}

func testDotenvRejected(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, ".env")
	if err := os.WriteFile(env, []byte("OPENAI_API_KEY=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FileSource(env); !errors.Is(err, ErrSource) {
		t.Fatalf("dotenv file accepted: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(dir, "linked.env")
	if err := os.Symlink(env, link); err != nil {
		t.Fatal(err)
	}
	if _, err := FileSource(link); !errors.Is(err, ErrSource) {
		t.Fatalf("dotenv symlink accepted: %v", err)
	}
}

func testEnvGrammar(t *testing.T) {
	for _, bad := range []string{".env", "OPENAI_API_KEY*", "../OPENAI_API_KEY", "openai_key"} {
		if _, err := EnvSource(bad); !errors.Is(err, ErrSource) {
			t.Errorf("EnvSource(%q) = %v, want ErrSource", bad, err)
		}
	}
}

// TestCredentialConfiguredSources (EC-SEC-003): explicit keychain, env and
// key_cmd sources work; keychain ids are path-free.
func TestCredentialConfiguredSources(t *testing.T) {
	g := phase.Current(t)
	if !g.Item(t, "EC-SEC-003", "F5", "covered") {
		return
	}
	t.Run("EC-SEC-003", func(t *testing.T) {
		t.Run("keychain", testKeychainSource)
		t.Run("env", testConfiguredEnv)
		t.Run("command rejects files", testCommandRejectsFiles)
	})
}

func testKeychainSource(t *testing.T) {
	src, err := KeychainSource("keychain:provider-alpha")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	r := &Resolver{Secrets: spyGetter{names: &names, value: "keychain-SECRET"}}
	if got, gerr := r.Resolve(context.Background(), src); gerr != nil || got != "keychain-SECRET" {
		t.Fatalf("keychain = %q, %v", got, gerr)
	}
	for _, bad := range []string{"keychain:", "keychain:../x", "plaintext", "keychain:a/b"} {
		if _, berr := KeychainSource(bad); !errors.Is(berr, ErrSource) {
			t.Errorf("KeychainSource(%q) = %v", bad, berr)
		}
	}
}

func testConfiguredEnv(t *testing.T) {
	src, err := EnvSource("PROVIDER_KEY")
	if err != nil {
		t.Fatal(err)
	}
	r := &Resolver{Getenv: func(name string) string {
		if name != "PROVIDER_KEY" {
			t.Fatalf("unexpected env lookup %q", name)
		}
		return "env-SECRET"
	}}
	if got, gerr := r.Resolve(context.Background(), src); gerr != nil || got != "env-SECRET" {
		t.Fatalf("env = %q, %v", got, gerr)
	}
}

func testCommandRejectsFiles(t *testing.T) {
	for _, argv := range [][]string{{"op", "read", ".env"}, {"sh", "-c", "cat ~/.aws/credentials"}} {
		if _, err := CommandSource(argv); !errors.Is(err, ErrSource) {
			t.Errorf("CommandSource(%v) = %v, want ErrSource", argv, err)
		}
	}
}

// TestCredentialKeyCmd: configured command runs once, is time-bounded, and
// truncates output; failures never echo the command's output.
func TestCredentialKeyCmd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	got, err := (&Resolver{Timeout: 60 * time.Second}).Resolve(ctx, commandSource(t, "ok"))
	if err != nil || got != "TOKEN-SECRET" {
		t.Fatalf("key_cmd ok = %q, %v", got, err)
	}
	_, err = (&Resolver{Timeout: 60 * time.Second}).Resolve(ctx, commandSource(t, "fail"))
	if !errors.Is(err, ErrCredential) || strings.Contains(err.Error(), "TOKEN-SECRET") || strings.Contains(err.Error(), "stderr") {
		t.Fatalf("key_cmd failure = %v", err)
	}
	_, err = (&Resolver{Timeout: 500 * time.Millisecond}).Resolve(ctx, commandSource(t, "block"))
	if !errors.Is(err, ErrCredential) {
		t.Fatalf("key_cmd timeout = %v", err)
	}
	got, err = (&Resolver{MaxBytes: 16, Timeout: 60 * time.Second}).Resolve(ctx, commandSource(t, "big"))
	if err != nil || len(got) != 16 {
		t.Fatalf("truncated output = %q (%d), %v", got, len(got), err)
	}
}
