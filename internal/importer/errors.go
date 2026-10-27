package importer

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by this package.
var (
	// ErrDeps reports a missing constructor dependency (E-11).
	ErrDeps = errors.New("importer: missing dependency")
	// ErrSyntax reports a source file that cannot be parsed.
	ErrSyntax = errors.New("importer: invalid source")
	// ErrSource reports a source item that cannot be imported.
	ErrSource = errors.New("importer: invalid item")
)

func syntaxError(path string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrSyntax, path, err)
}
