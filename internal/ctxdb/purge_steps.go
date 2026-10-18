package ctxdb

import (
	"errors"
	"io/fs"
)

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
	for i := 0; err == nil && i < len(shas); i++ {
		if _, serr := t.s.root.Stat(blobName(shas[i])); !errors.Is(serr, fs.ErrNotExist) {
			err = t.trashFile(shas[i])
		}
		if err == nil {
			err = t.run("purge.drop_blob", shas[i])
		}
	}
	return err
}
