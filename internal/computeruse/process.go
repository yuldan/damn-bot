package computeruse

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/damnbot/damnbot/internal/modules/host"
)

type processBackend struct {
	path        string
	baseArgs    []string
	env         []string
	spawn       func(context.Context, host.SpawnSpec) (host.Process, error)
	max         int
	unavailable error
}

func newProcessBackend(cfg *Config, deps Deps) (*processBackend, error) {
	b := &processBackend{spawn: deps.Spawn, max: cfg.MaxScreenshotBytes}
	if len(cfg.Command) == 0 {
		return b, nil
	}
	if len(cfg.Command) > 16 || !filepath.IsAbs(cfg.Command[0]) {
		return nil, fmt.Errorf("%w: command[0] must be absolute in an argv of at most 16", ErrInvalid)
	}
	b.path, b.baseArgs = cfg.Command[0], append([]string(nil), cfg.Command[1:]...)
	b.env = []string{}
	if len(cfg.Env) > 0 {
		env, err := deps.Secrets.ChildEnv(host.AllowPolicy{Inherit: append([]string(nil), cfg.Env...)})
		if err != nil {
			b.unavailable = fmt.Errorf("%w: child environment: %w", ErrUnavailable, err)
			return b, nil
		}
		b.env = env
	}
	return b, nil
}

func (b *processBackend) Start(context.Context) error {
	if b.unavailable != nil {
		return b.unavailable
	}
	if b.path == "" || b.spawn == nil {
		return fmt.Errorf("%w: no command or spawner", ErrUnavailable)
	}
	if _, err := os.Stat(b.path); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrUnavailable, b.path, err)
	}
	return nil
}

func (b *processBackend) Act(ctx context.Context, a *Action) (Result, error) {
	if a.Kind == ActionScreenshot {
		png, err := b.Screenshot(ctx)
		if err != nil {
			return Result{}, err
		}
		return Result{Image: png}, nil
	}
	args, stdin, err := b.argv(a)
	if err != nil {
		return Result{}, err
	}
	out, err := b.run(ctx, args, stdin, maxOutput)
	if err != nil {
		return Result{}, err
	}
	return Result{Output: string(out)}, nil
}

func (b *processBackend) Screenshot(ctx context.Context) ([]byte, error) {
	return b.run(ctx, []string{"screenshot"}, nil, b.max)
}

func (b *processBackend) Stop(context.Context) error { return nil }

func (b *processBackend) argv(a *Action) ([]string, []byte, error) {
	switch a.Kind {
	case ActionMove:
		return []string{"move", "--x", strconv.Itoa(a.X), "--y", strconv.Itoa(a.Y)}, nil, nil
	case ActionClick:
		return []string{
			"click", "--x", strconv.Itoa(a.X), "--y", strconv.Itoa(a.Y),
			"--button", cmp.Or(a.Button, "left"),
		}, nil, nil
	case ActionType:
		return []string{"type"}, []byte(a.Text), nil
	case ActionKey:
		return []string{"key", "--key", a.Key}, nil, nil
	case ActionScroll:
		return []string{"scroll", "--dx", strconv.Itoa(a.DX), "--dy", strconv.Itoa(a.DY)}, nil, nil
	case ActionWait:
		return []string{"wait", "--ms", strconv.Itoa(a.WaitMS)}, nil, nil
	case ActionScreenshot:
		return []string{"screenshot"}, nil, nil
	}
	return nil, nil, fmt.Errorf("%w: action %q", ErrInvalid, a.Kind)
}

type stdioPipes interface {
	Stdio() (stdin io.WriteCloser, stdout, stderr io.Reader)
}

func (b *processBackend) run(ctx context.Context, args []string, stdin []byte, limit int) ([]byte, error) {
	spec := host.SpawnSpec{Path: b.path, Args: append(append([]string(nil), b.baseArgs...), args...), Env: append([]string(nil), b.env...)}
	p, err := b.spawn(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnavailable, b.path, err)
	}
	pipes, ok := p.(stdioPipes)
	if !ok {
		return nil, b.abort(p, fmt.Errorf("%w: no stdio pipes", ErrUnavailable))
	}
	in, out, errp := pipes.Stdio()
	if in == nil || out == nil {
		return nil, b.abort(p, fmt.Errorf("%w: nil stdio pipes", ErrUnavailable))
	}
	if errp != nil {
		go func() {
			if _, copyErr := io.Copy(io.Discard, io.LimitReader(errp, 4096)); copyErr != nil {
				return
			}
		}()
	}
	if len(stdin) > 0 {
		if _, werr := in.Write(stdin); werr != nil {
			return nil, b.abort(p, fmt.Errorf("%w: stdin: %w", ErrBackend, werr))
		}
	}
	if cerr := in.Close(); cerr != nil {
		return nil, b.abort(p, fmt.Errorf("%w: stdin close: %w", ErrBackend, cerr))
	}
	data, rerr := io.ReadAll(io.LimitReader(out, int64(limit)+1))
	if rerr != nil {
		return nil, b.abort(p, fmt.Errorf("%w: read: %w", ErrBackend, rerr))
	}
	if len(data) > limit {
		return nil, b.abort(p, fmt.Errorf("%w: output %d > %d bytes", ErrTooLarge, len(data), limit))
	}
	werr := p.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())
	}
	if werr != nil {
		return nil, fmt.Errorf("%w: exit: %w", ErrBackend, werr)
	}
	return data, nil
}

func (b *processBackend) abort(p host.Process, cause error) error {
	if kerr := p.Kill(); kerr != nil {
		return errors.Join(cause, kerr)
	}
	if werr := p.Wait(); werr != nil {
		return errors.Join(cause, werr)
	}
	return cause
}
