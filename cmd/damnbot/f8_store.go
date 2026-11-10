//go:build !minimal

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// f8Unavail builds the typed refusal.
func f8Unavail(module, reason string) error {
	return &errF8Unavailable{module: module, reason: reason}
}

func f8BadArg(format string, args ...any) error {
	return &errF8BadArg{msg: fmt.Sprintf(format, args...)}
}

// f8StorePath names one JSON store below the data directory.
func f8StorePath(dir, name string) string { return filepath.Join(dir, name) }

// f8Load decodes one JSON store; a missing file is the zero value.
func f8Load(path string, v any) error {
	b, err := os.ReadFile(path) // #nosec G304 -- path is <data_dir>/<fixed store name>
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

// f8Save writes one JSON store with mode 0600. The write is in place: these
// stores are small operator state, not crash-critical journals.
func f8Save(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	b = append(b, '\n')
	if merr := os.MkdirAll(filepath.Dir(path), 0o700); merr != nil {
		return fmt.Errorf("mkdir store: %w", merr)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- fixed store name
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	if err := f.Chmod(0o600); err != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("chmod %s: %w", filepath.Base(path), errors.Join(err, cerr))
		}
		return fmt.Errorf("chmod %s: %w", filepath.Base(path), err)
	}
	if _, err := f.Write(b); err != nil {
		if cerr := f.Close(); cerr != nil {
			return fmt.Errorf("write %s: %w", filepath.Base(path), errors.Join(err, cerr))
		}
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", filepath.Base(path), err)
	}
	return nil
}
