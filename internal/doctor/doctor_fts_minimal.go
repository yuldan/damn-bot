//go:build minimal

package doctor

import "context"

func checkFTS(context.Context, *Options) Check {
	return Check{"DOC-014", "fts index", StatusOK, "not checked in minimal build"}
}
