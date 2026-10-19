//go:build !unix

package sandbox

import (
	"context"
	"os"
	"path/filepath"
)

// Open on platforms without openat falls back to OpenResolved on the full
// path: same sandbox rules, just without the one-component fast path.
func (d *Dir) Open(name string) (*os.File, Verdict, error) {
	if err := dirChild(name); err != nil {
		return nil, Inside, err
	}
	return d.sb.OpenResolved(context.Background(), filepath.Join(d.path, name), false)
}
