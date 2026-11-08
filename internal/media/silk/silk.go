// Package silk decodes SILK/SILKv3 voice payloads to PCM16 through an external
// decoder executable (ADR-001): no CGO and no codec library is linked. The
// decoder reads the payload on stdin and writes PCM/WAV on stdout; output is
// buffered and only copied on a clean exit, so a failed decode writes 0 bytes.
package silk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Decoder failures. Compared with errors.Is.
var (
	ErrDecoderMissing = errors.New("silk: module_unavailable{silk_decoder_missing}")
	ErrSilkDecoder    = errors.New("silk: decoder failed to start")
	ErrSilkCorrupt    = errors.New("silk: payload rejected by the decoder")
)

// Default limits.
const (
	MaxOutputBytes = 32 << 20 // 32 MiB PCM16 ≈ 5.8 min at 48 kHz mono
	MaxStderrBytes = 512
)

// Config is the explicit decoder command (argv, no shell). An empty command
// fails closed: nothing is executed and no payload is copied.
type Config struct {
	Argv    []string
	Env     []string
	Timeout time.Duration
}

// Decode runs the decoder on payload and copies the PCM/WAV output to out.
func Decode(ctx context.Context, payload []byte, out io.Writer, cfg *Config) error {
	if cfg == nil || len(cfg.Argv) == 0 || cfg.Argv[0] == "" {
		return ErrDecoderMissing
	}
	if out == nil {
		return fmt.Errorf("%w: nil output", ErrSilkDecoder)
	}
	binary, err := exec.LookPath(cfg.Argv[0])
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDecoderMissing, err)
	}
	if cfg.Timeout <= 0 {
		cfg = &Config{Argv: cfg.Argv, Env: cfg.Env, Timeout: 60 * time.Second}
	}
	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary, cfg.Argv[1:]...) // #nosec G204 -- explicit argv seam, no shell
	cmd.Env = cfg.Env
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{dst: &stdout, max: MaxOutputBytes}
	cmd.Stderr = &limitedWriter{dst: &stderr, max: MaxStderrBytes}
	if err := cmd.Run(); err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return fmt.Errorf("%w: exit %d", ErrSilkCorrupt, exit.ExitCode())
		}
		return fmt.Errorf("%w: %w", ErrSilkDecoder, err)
	}
	if stdout.Len() == 0 {
		return fmt.Errorf("%w: decoder produced no audio", ErrSilkCorrupt)
	}
	if stdout.Len()%2 != 0 {
		return fmt.Errorf("%w: odd PCM16 length %d", ErrSilkCorrupt, stdout.Len())
	}
	if _, err := io.Copy(out, &stdout); err != nil {
		return fmt.Errorf("silk: write output: %w", err)
	}
	return nil
}

// limitedWriter stops accepting bytes past max and reports a decode failure.
type limitedWriter struct {
	dst io.Writer
	max int
	n   int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.n+len(p) > w.max {
		return 0, fmt.Errorf("%w: output exceeds %d bytes", ErrSilkCorrupt, w.max)
	}
	n, err := w.dst.Write(p)
	w.n += n
	if err != nil {
		return n, fmt.Errorf("silk: output: %w", err)
	}
	return n, nil
}
