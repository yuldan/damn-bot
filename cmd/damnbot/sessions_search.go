// `damnbot sessions search <query>` (SPEC §10, §4.6): FTS5 recall over the
// session store, read-only. Output is one tab-separated row per hit:
// session_id, seq, kind, snippet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

type searchArgs struct {
	dataDir, session, cwd string
	limit                 int
	query                 string
}

// parseSearchArgs: the query is the positional argument (words joined);
// flags may appear before or after it.
func parseSearchArgs(args []string, stderr io.Writer) (searchArgs, error) {
	var a searchArgs
	fs := flag.NewFlagSet("sessions search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.session, "session", "", "only this session id")
	fs.StringVar(&a.cwd, "cwd", "", "only sessions created in this directory")
	fs.IntVar(&a.limit, "limit", 0, "maximum hits (default 10, maximum 1000)")
	fs.StringVar(&a.dataDir, "data-dir", "", "data directory (default: SPEC §11)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return searchArgs{}, err
	}
	if len(pos) == 0 {
		return searchArgs{}, errors.New("a query is required")
	}
	if a.limit < 0 {
		return searchArgs{}, fmt.Errorf("invalid --limit %d (want >= 0)", a.limit)
	}
	a.query = strings.Join(pos, " ")
	return a, nil
}

// sessionsSearch prints the best hits first (bm25). The store already drops
// purging sessions; the snippet is post-redaction content, and terminal
// control bytes are removed before printing.
func sessionsSearch(args []string, stdout, stderr io.Writer) int {
	a, err := parseSearchArgs(args, stderr)
	if err != nil {
		return usageError(stderr, "sessions search", err)
	}
	ctx := context.Background()
	db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "search", derr, a.session)
	}
	defer closeSessionsDB(ctx, db, "sessions search", stderr)
	hits, serr := db.Search(ctx, ctxdb.Query{Text: a.query, SessionID: a.session, Cwd: a.cwd, Limit: a.limit})
	if serr != nil {
		return sessionsFail(stderr, "search", serr, a.session)
	}
	for i := range hits {
		h := &hits[i]
		note := ""
		if h.Excluded {
			note = " [excluded: context_exclude]" // SEC11-5
		}
		sessionsPrintf(stdout, "%s\t%d\t%s\t%s%s\n",
			cleanArg(h.SessionID), h.Seq, cleanArg(h.Kind), cleanLine(h.Snippet), note)
	}
	return 0
}
