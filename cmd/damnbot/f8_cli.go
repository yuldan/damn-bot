//go:build !minimal

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/config"
)

// Exit codes of the F8 one-shot CLI surfaces (parallel to `damnbot cua`).
// Usage and invalid arguments are always 2; a module that is not wired in
// this build is 3 and prints a typed module_unavailable line.
const (
	f8ExitOK          = 0
	f8ExitFail        = 1
	f8ExitUsage       = 2
	f8ExitUnavailable = 3
)

// errF8Unavailable is the typed refusal of an F8 surface whose backing
// module (OAuth proxy, external memory provider, hosted connector accounts,
// petdex fetch) is not part of this build. It is never a fake success.
type errF8Unavailable struct{ module, reason string }

func (e *errF8Unavailable) Error() string {
	return fmt.Sprintf("module_unavailable: %s: %s", e.module, e.reason)
}

type errF8BadArg struct{ msg string }

func (e *errF8BadArg) Error() string { return e.msg }

// f8CmdFail maps one command error onto the F8 exit codes. Runtime errors are 1
// and never print a stack or a raw path from the journal.
func f8CmdFail(stderr io.Writer, name string, err error) int {
	code := f8ExitFail
	if _, ok := errors.AsType[*errF8Unavailable](err); ok {
		code = f8ExitUnavailable
	}
	if _, ok := errors.AsType[*errF8BadArg](err); ok {
		code = f8ExitUsage
	}
	if say(stderr, "%s: %s\n", name, cleanLine(err.Error())) != 0 {
		return code
	}
	return code
}

// f8Dir resolves the data directory: an explicit --data-dir wins, otherwise
// the profile paths of `damnbot run` decide. Nothing is created here.
func f8Dir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return paths.DataDir, nil
}

// f8WriteFile writes one artifact 0600 and returns its byte count.
func f8WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir artifact: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- caller-chosen artifact
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("chmod artifact: %w", errors.Join(err, cerr))
		}
		return fmt.Errorf("chmod artifact: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("write artifact: %w", errors.Join(err, cerr))
		}
		return fmt.Errorf("write artifact: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close artifact: %w", err)
	}
	return nil
}

// errF8Usage marks a usage error whose line was already printed; the caller
// answers exit 2 without repeating it.
var errF8Usage = errors.New("usage")
