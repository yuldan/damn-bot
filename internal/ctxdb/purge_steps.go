package ctxdb

import (
	"os"
	"strconv"
	"strings"
)

// loadQueries parses queries.sql into named blocks ("-- name: <id>").
func loadQueries() (map[string]string, error) {
	b, err := migFS.ReadFile("queries.sql")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, blk := range strings.Split(string(b), "-- name: ")[1:] {
		name, body, _ := strings.Cut(blk, "\n")
		out[strings.TrimSpace(name)] = body
	}
	for k, v := range out {
		out[k] = strings.ReplaceAll(v, "/*LIVE_REF*/", out["gc.live_ref"])
	}
	return out, nil
}

// run executes a named block (one or more statements, same arguments).
func (t *Tx) run(name string, args ...any) error {
	_, err := t.Exec(t.s.q[name], args...)
	return err
}

// Ints returns the first integer column of every row.
func (t *Tx) Ints(q string, args ...any) ([]int64, error) {
	ss, err := t.Strings(q, args...)
	out := make([]int64, 0, len(ss))
	for _, s := range ss {
		n, _ := strconv.ParseInt(s, 10, 64)
		out = append(out, n)
	}
	return out, err
}

// purgeFinal = phase 3 for sessions sb: defensive repeat of steps 10–24 and
// 1–3 (normally no-ops thanks to I3), then 27–30, so step 30 never sticks
// on an FK (I10, TestPurgeNeverStuck).
func (t *Tx) purgeFinal(sb string, audit int64) error {
	ev, err := t.Ints("SELECT id FROM events WHERE session_id IN (SELECT value FROM json_each(?1))", sb)
	if err == nil {
		err = t.run("purge.events", jsonOf(ev), audit)
	}
	if err == nil {
		err = t.run("purge.release", sb)
	}
	if err == nil {
		err = t.run("purge.final", sb, audit)
	}
	return err
}

// freeBlobs = step 25: every sha whose refs were deleted in this transaction
// and now has none goes to .trash/<txid>/ and loses its rows.
func (t *Tx) freeBlobs() error {
	shas, err := t.Strings(t.s.q["purge.freed"])
	for _, sha := range shas {
		if err != nil {
			break
		}
		if _, serr := os.Stat(t.s.blobPath(sha)); serr == nil {
			err = t.trashFile(sha)
		}
		if err == nil {
			err = t.run("purge.drop_blob", sha)
		}
	}
	return err
}
