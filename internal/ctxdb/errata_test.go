package ctxdb

import (
	"context"
	"testing"
)

// TestErrataV13IdentityStrengthRequired (V-13, ADR-054): gateway_identities
// has no DEFAULT for `strength`, so an adapter that forgets to set it gets a
// NOT NULL error (fail-closed) instead of a platform_authenticated identity.
func TestErrataV13IdentityStrengthRequired(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	err := s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, status, updated_at) VALUES ('irc', 'nick', 'pending', 0)`)
		return err
	})
	if err == nil {
		t.Fatal("identity without strength accepted (fail-open)")
	}
	if n := count(t, s, "SELECT count(*) FROM gateway_identities"); n != 0 {
		t.Fatalf("%d identities stored after the rejected insert", n)
	}
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, status, strength, updated_at) VALUES ('irc', 'nick', 'pending', 'weak', 0)`)
		return err
	}))
	var strength string
	must(t, s.get(context.Background(), "SELECT strength FROM gateway_identities WHERE external_id = 'nick'", nil, &strength))
	if strength != "weak" {
		t.Fatalf("strength = %q, want weak", strength)
	}
}

// TestErrataV15SkillNameNormalised (V-15, ADR-054): skills_ai/skills_au index
// `name` through dmb_fts_norm like description/body, so a CJK skill name is
// found by a normalised (bigram) query, also after a rename.
func TestErrataV15SkillNameNormalised(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO skills(name, path, sha256, description, body, source, indexed_at)
			VALUES ('天气预报', '/s', 'x', 'd', '天气预报', 'user', 0)`)
		return err
	}))
	hits := func(col, q string) int64 {
		return count(t, s, "SELECT count(*) FROM skills_fts WHERE skills_fts MATCH '"+col+" : ' || dmb_fts_norm('"+q+"')")
	}
	if inName, inBody := hits("name", "天气预报"), hits("body", "天气预报"); inName != 1 || inBody != 1 {
		t.Fatalf("name hits %d, body hits %d (want 1, 1)", inName, inBody)
	}
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`UPDATE skills SET name = '股票行情' WHERE path = '/s'`)
		return err
	}))
	if oldName, newName := hits("name", "天气预报"), hits("name", "股票行情"); oldName != 0 || newName != 1 {
		t.Fatalf("after rename: old name hits %d, new name hits %d (want 0, 1)", oldName, newName)
	}
}

// TestPurgeFTSIntegrity: purging events that were never FTS-indexed
// (turn_start, system kinds) must not touch the contentless FTS index;
// deleting a missing rowid there corrupts it (found by TestKill9Chaos).
func TestPurgeFTSIntegrity(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	session(t, s, "S", "")
	appendMsg(t, s, "S", "indexed text")
	must(t, s.Write(context.Background(), func(tx *Tx) error { return tx.StartTurn("S", "T1") }))
	must(t, s.Write(context.Background(), func(tx *Tx) error { return tx.EndTurn("S", "T1", "end_turn") }))
	must(t, s.ReleaseLease(context.Background(), "S"))
	_, err := s.Purge(context.Background(), PurgeRequest{IDs: []string{"S"}})
	must(t, err)
	checkIntegrity(t, s)
	// events.id is a plain INTEGER PRIMARY KEY: ids freed by purge are reused.
	session(t, s, "R", "")
	for range 5 {
		appendMsg(t, s, "R", "reuses purged ids")
	}
	checkIntegrity(t, s)
	if n := count(t, s, "SELECT count(*) FROM events_fts WHERE events_fts MATCH 'reuses'"); n != 5 {
		t.Fatalf("fts hits %d, want 5", n)
	}
}

// TestIntegrityCheckFreshConnection (TS-P11): after a reader connection has
// run an FTS5 integrity check and a purge rewrote the contentless_delete
// index, IntegrityCheck must report ok (it uses a fresh connection).
func TestIntegrityCheckFreshConnection(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	for i := range 40 {
		sid := "S" + itoa(int64(i))
		session(t, s, sid, "")
		for j := range 30 {
			appendMsg(t, s, sid, "word"+itoa(int64(j))+" common text")
		}
		must(t, s.ReleaseLease(ctx, sid))
	}
	var pooled string
	must(t, s.get(ctx, "PRAGMA integrity_check", nil, &pooled)) // warms the pooled reader's FTS5 cache
	for i := 0; i < 40; i += 2 {
		_, err := s.Purge(ctx, PurgeRequest{IDs: []string{"S" + itoa(int64(i))}})
		must(t, err)
	}
	must(t, s.get(ctx, "PRAGMA integrity_check", nil, &pooled))
	fresh, err := s.IntegrityCheck(ctx)
	must(t, err)
	t.Logf("pooled reader: %s", pooled)
	if fresh != "ok" {
		t.Fatalf("fresh integrity_check: %s", fresh)
	}
}
