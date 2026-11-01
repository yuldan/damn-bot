//go:build !minimal

package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrSource rejects a credential source that is not explicitly allowed.
var ErrSource = errors.New("auth: credential source rejected")

// SourceKind is one explicitly configured credential source. There is no
// file/dotenv kind: EC-SEC-002/003 forbid implicit discovery.
type SourceKind uint8

// Allowed source kinds.
const (
	SourceEnv SourceKind = iota + 1
	SourceKeychain
	SourceCommand
)

// Source names exactly one allowed credential source.
type Source struct {
	Kind SourceKind
	Name string
	Argv []string
}

// EnvSource accepts one exact environment-variable name, never a glob, path
// or dotenv name.
func EnvSource(name string) (Source, error) {
	if !validEnvCredentialName(name) {
		return Source{}, fmt.Errorf("%w: invalid environment name %q", ErrSource, name)
	}
	return Source{Kind: SourceEnv, Name: name}, nil
}

// KeychainSource accepts a keychain:<id> reference with a path-free id.
func KeychainSource(ref string) (Source, error) {
	id, ok := strings.CutPrefix(ref, "keychain:")
	if !ok || id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\\x00") || id == "." || id == ".." {
		return Source{}, fmt.Errorf("%w: invalid keychain reference", ErrSource)
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f {
			return Source{}, fmt.Errorf("%w: invalid keychain reference", ErrSource)
		}
	}
	return Source{Kind: SourceKeychain, Name: id}, nil
}

// CommandSource accepts one configured argv. Shell is never used, output is
// bounded by [Resolver.MaxBytes], and credential-file/dotenv arguments are
// refused rather than executed.
func CommandSource(argv []string) (Source, error) {
	if len(argv) == 0 || len(argv) > 32 {
		return Source{}, fmt.Errorf("%w: key_cmd needs 1..32 arguments", ErrSource)
	}
	for _, arg := range argv {
		if arg == "" || len(arg) > 4096 || strings.ContainsRune(arg, 0) || suspiciousCredentialPath(arg) {
			return Source{}, fmt.Errorf("%w: invalid key_cmd argument", ErrSource)
		}
	}
	if strings.ContainsAny(argv[0], "/\\") {
		fi, err := os.Lstat(argv[0])
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return Source{}, fmt.Errorf("%w: key_cmd executable is not a regular file", ErrSource)
		}
	}
	return Source{Kind: SourceCommand, Argv: append([]string(nil), argv...)}, nil
}

// FileSource always fails: file, dotenv and symlinked credential stores are
// outside the discovery policy even when they exist in the workspace.
func FileSource(path string) (Source, error) {
	return Source{}, fmt.Errorf("%w: file credential %q is never discovered", ErrSource, redactPath(path))
}

func suspiciousCredentialPath(arg string) bool {
	base := strings.ToLower(filepath.Base(arg))
	switch base {
	case ".env", ".netrc", ".git-credentials", "credentials", ".credentials.json":
		return true
	}
	lower := strings.ToLower(arg)
	return strings.Contains(lower, "/.aws/") || strings.Contains(lower, "/.claude/") ||
		strings.HasPrefix(lower, "file:") || strings.Contains(lower, ".env/")
}

func redactPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

func validEnvCredentialName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
		if i == 0 && r >= '0' && r <= '9' {
			return false
		}
	}
	return true
}

// SecretGetter is the keychain subset the resolver needs.
type SecretGetter interface {
	Get(ctx context.Context, name string) (string, error)
}

// Resolver reads one explicitly configured source. Implicit discovery is
// impossible: there is no ambient/catalog/dotenv lookup, and Getenv is called
// with exactly the configured name.
type Resolver struct {
	Getenv     func(string) string
	Secrets    SecretGetter
	Timeout    time.Duration
	MaxBytes   int
	CommandEnv []string
	Run        func(context.Context, []string, int) (string, error)
}

const (
	defaultCredentialTimeout = 5 * time.Second
	defaultCredentialBytes   = 4 << 10
)

// Resolve returns one secret or a value-free error.
func (r *Resolver) Resolve(ctx context.Context, src Source) (string, error) {
	switch src.Kind {
	case SourceEnv:
		if r.Getenv == nil {
			return "", fmt.Errorf("%w: environment source needs a lookup", ErrCredential)
		}
		v := r.Getenv(src.Name)
		if strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("%w: environment %s is not set", ErrCredential, src.Name)
		}
		return v, nil
	case SourceKeychain:
		if r.Secrets == nil {
			return "", fmt.Errorf("%w: keychain source needs a secret store", ErrCredential)
		}
		v, err := r.Secrets.Get(ctx, src.Name)
		if err != nil {
			return "", fmt.Errorf("%w: keychain %s: %w", ErrCredential, src.Name, err)
		}
		if strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("%w: keychain %s is empty", ErrCredential, src.Name)
		}
		return v, nil
	case SourceCommand:
		v, err := r.runCommand(ctx, src.Argv)
		if err != nil {
			return "", err
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return "", fmt.Errorf("%w: key_cmd returned no value", ErrCredential)
		}
		return v, nil
	default:
		return "", fmt.Errorf("%w: unknown source kind", ErrSource)
	}
}

func (r *Resolver) runCommand(ctx context.Context, argv []string) (string, error) {
	limit := r.MaxBytes
	if limit <= 0 {
		limit = defaultCredentialBytes
	}
	if r.Run != nil {
		return r.Run(ctx, argv, limit)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultCredentialTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...) // #nosec G204 -- argv is validated config, never shell
	cmd.Env = append(os.Environ(), r.CommandEnv...)
	cmd.Stderr = io.Discard
	buf := &limitedBuffer{max: limit}
	cmd.Stdout = buf
	if err := cmd.Run(); err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: key_cmd timed out", ErrCredential)
		}
		return "", fmt.Errorf("%w: key_cmd failed", ErrCredential)
	}
	return buf.String(), nil
}

// limitedBuffer keeps at most max bytes and reports a full write so the
// child never sees EPIPE; the tail is discarded.
type limitedBuffer struct {
	buf []byte
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := b.max - len(b.buf)
	if room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return string(b.buf) }
