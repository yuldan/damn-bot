//go:build !minimal

package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// checkFTS reports GAP-INF-02 re-index progress. A running upgrade is a
// warning (searches still answer, but the index is partial).
func checkFTS(ctx context.Context, o *Options) Check {
	const id, name = "DOC-014", "fts index"
	path := ctxdb.DBPath(o.DataDir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return Check{id, name, StatusOK, "not created"}
	} else if err != nil {
		return Check{id, name, StatusWarn, fmt.Sprintf("%s: %v", path, err)}
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: o.DataDir, Kind: "doctor", ReadOnly: true})
	if err != nil {
		return Check{id, name, StatusWarn, fmt.Sprintf("%s: %v", path, err)}
	}
	sts, serr := db.FTSStatus(ctx)
	cerr := db.CloseContext(ctx)
	if serr == nil {
		serr = cerr
	}
	if serr != nil {
		return Check{id, name, StatusWarn, fmt.Sprintf("%s: %v", path, serr)}
	}
	var parts []string
	for _, st := range sts {
		if st.Status != "done" {
			parts = append(parts, fmt.Sprintf("%s reindexing cursor %d/%d rows", st.Name, st.ReindexedTo, st.Total))
		}
	}
	if len(parts) > 0 {
		return Check{id, name, StatusWarn, strings.Join(parts, "; ")}
	}
	return Check{id, name, StatusOK, fmt.Sprintf("%d indexes current", len(sts))}
}
