package config

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"testing"
)

func testLoader(t *testing.T, files map[string][]byte) *Loader {
	t.Helper()
	l, err := New(Deps{
		ReadFile: func(name string) ([]byte, error) {
			if data, ok := files[name]; ok {
				return data, nil
			}
			return nil, fs.ErrNotExist
		},
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return "/home/u", nil },
		GOOS:    "linux",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func parse(t *testing.T, toml string) (Config, error) {
	t.Helper()
	return testLoader(t, nil).Parse([]byte(toml), "config.toml")
}

// hostPath is an absolute path for the host GOOS; filepath.IsAbs drives the
// skills check, so a literal "/p" is rejected on Windows.
func hostPath(t *testing.T) string {
	t.Helper()
	return filepath.ToSlash(t.TempDir())
}

func tomlQuote(p string) string { return strconv.Quote(p) }

func tomlAbsPath(t *testing.T) string {
	t.Helper()
	return tomlQuote(hostPath(t))
}

func requireKeyError(t *testing.T, err, kind error) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %v", kind)
	}
	if _, ok := errors.AsType[*Error](err); !ok {
		t.Fatalf("error %v is not *Error", err)
	}
	if !errors.Is(err, kind) {
		t.Fatalf("error %v does not match %v", err, kind)
	}
}

func keyErrorOf(t *testing.T, err, kind error) *Error {
	t.Helper()
	requireKeyError(t, err, kind)
	ke, _ := errors.AsType[*Error](err)
	return ke
}
