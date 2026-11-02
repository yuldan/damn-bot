//go:build minimal

package code

import (
	"context"
	"fmt"
)

// starlarkCommand is unavailable in the minimal build.
func (r *Runner) starlarkCommand() ([]string, error) {
	return nil, fmt.Errorf("%w: starlark is unavailable in the minimal build", ErrLanguage)
}

// startToolServer is absent in the minimal build; Run refuses Starlark before
// reaching it.
func (r *Runner) startToolServer(context.Context, string, string) (string, func(), error) {
	return "", func() {}, fmt.Errorf("%w: starlark is unavailable in the minimal build", ErrLanguage)
}
