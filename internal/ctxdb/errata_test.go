package ctxdb

import (
	"context"
	"testing"
)

// TestErrataV13IdentityStrengthDefault pins the frozen DDL behaviour behind
// V-13: a gateway_identities row inserted without `strength` is treated as
// platform_authenticated (fail-open). The proposed errata ADR changes the
// DDL to require the column; this test then flips to expect a NOT NULL error.
func TestErrataV13IdentityStrengthDefault(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, status, updated_at) VALUES ('irc', 'nick', 'pending', 0)`)
		return err
	}))
	var strength string
	must(t, s.get(context.Background(), "SELECT strength FROM gateway_identities WHERE external_id = 'nick'", nil, &strength))
	if strength != "platform_authenticated" {
		t.Fatalf("strength default = %q", strength)
	}
}

// TestErrataV15SkillNameNotNormalised pins V-15: skills_ai indexes `name`
// without dmb_fts_norm, so a CJK skill name is not found by a normalised
// (bigram) query, while the same text in `body` is.
func TestErrataV15SkillNameNotNormalised(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	must(t, s.Write(context.Background(), func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO skills(name, path, sha256, description, body, source, indexed_at)
			VALUES ('天气预报', '/s', 'x', 'd', '天气预报', 'user', 0)`)
		return err
	}))
	inName := count(t, s, "SELECT count(*) FROM skills_fts WHERE skills_fts MATCH 'name : ' || dmb_fts_norm('天气预报')")
	inBody := count(t, s, "SELECT count(*) FROM skills_fts WHERE skills_fts MATCH 'body : ' || dmb_fts_norm('天气预报')")
	if inName != 0 || inBody != 1 {
		t.Fatalf("name hits %d, body hits %d (want 0, 1 while V-15 is open)", inName, inBody)
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
