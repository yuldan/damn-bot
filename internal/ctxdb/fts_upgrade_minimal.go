//go:build minimal

package ctxdb

import "context"

func (s *Store) startFTSUpgrade(context.Context, bool) {}
