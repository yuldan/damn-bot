package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
)

// RunFunc runs one command and returns its bounded stdout; tests inject it.
type RunFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// cliEngine is the Lightpanda CLI (ADR-321): fetch-only. Lightpanda has no
// interactive page, so input, evaluation and screenshots fail with
// errUnsupported -> module_unavailable instead of pretending to succeed.
type cliEngine struct {
	bin string
	cfg Config
	run RunFunc
}

func newCLIEngine(bin string, cfg Config, run RunFunc) (*cliEngine, error) {
	if bin == "" {
		return nil, fmt.Errorf("%w: lightpanda_bin is empty", errUnavailable)
	}
	if !filepath.IsAbs(bin) {
		return nil, fmt.Errorf("%w: lightpanda_bin must be absolute", errBadConfig)
	}
	if run == nil {
		run = execRun(cfg.MaxResult)
	}
	return &cliEngine{bin: bin, cfg: cfg, run: run}, nil
}

// Navigate fetches one page with `lightpanda fetch --dump`, bounded by
// MaxResult. The guard runs before the call; lightpanda follows redirects
// internally and reports no final URL.
func (e *cliEngine) Navigate(ctx context.Context, rawURL string, guard func(*url.URL) error) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%w: %s", errBadConfig, rawURL)
	}
	if gerr := guard(u); gerr != nil {
		return "", gerr
	}
	out, err := e.run(ctx, e.bin, "fetch", "--dump", u.String())
	if err != nil {
		return "", err
	}
	if len(out) > e.cfg.MaxResult {
		return "", fmt.Errorf("%w: page %d > %d bytes", errTooLarge, len(out), e.cfg.MaxResult)
	}
	return u.String(), nil
}

func (e *cliEngine) WaitLoad(context.Context) error { return nil }

func (e *cliEngine) Eval(context.Context, string) (json.RawMessage, error) {
	return nil, e.unsupported("eval")
}

func (e *cliEngine) Screenshot(context.Context) ([]byte, error) {
	return nil, e.unsupported("screenshot")
}

func (e *cliEngine) Act(context.Context, Action) error { return e.unsupported("action") }

func (e *cliEngine) Close() error { return nil }

func (e *cliEngine) unsupported(op string) error {
	return fmt.Errorf("%w: lightpanda %s", errUnsupported, op)
}

// execRun runs one command without a shell. Extra output is refused, not
// truncated: a truncated dump would look like a successful page.
func execRun(max int) RunFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- absolute configured binary and validated URL argument
		out := &capWriter{max: max}
		cmd.Stdout = out
		cmd.Stderr = io.Discard
		err := cmd.Run()
		if errors.Is(err, errTooLarge) {
			return nil, errTooLarge
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("%w: lightpanda: %w", errTimeout, ctx.Err())
			}
			return nil, fmt.Errorf("browser: lightpanda: %w", err)
		}
		return out.buf.Bytes(), nil
	}
}

type capWriter struct {
	buf bytes.Buffer
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		return 0, errTooLarge
	}
	n, err := w.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("%w: buffer: %w", errTooLarge, err)
	}
	return n, nil
}
