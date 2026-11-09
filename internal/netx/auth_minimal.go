//go:build minimal

package netx

import "context"

// WithAuthHeaders is a no-op in the minimal build: the Tool DLP gate and its
// auth-header exemption are not compiled in.
func WithAuthHeaders(ctx context.Context, names ...string) context.Context { return ctx }
