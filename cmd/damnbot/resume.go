package main

import (
	"context"
	"fmt"
	"os"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// runSession creates a new headless session, or resumes --session /
// --continue. A resumed session keeps its stored cwd and model (the model is
// used only when the caller did not pass one) and holds its lease until run
// ends.
func runSession(ctx context.Context, db *ctxdb.Store, modelRef string, o *runOptions) (sid, cwd, storedModel string, err error) {
	if o.session == "" && !o.cont {
		sid, cwd, err = newRunSession(ctx, db, modelRef)
		return sid, cwd, modelRef, err
	}
	sid, cwd, storedModel, err = resumeTarget(ctx, db, o)
	if err != nil {
		return "", "", "", err
	}
	if _, err = db.AcquireLease(ctx, sid); err != nil {
		return "", "", "", fmt.Errorf("resume session %s: %w", sid, err)
	}
	return sid, cwd, storedModel, nil
}

// resumeTarget resolves --session or --continue to its id and stored cwd/model.
func resumeTarget(ctx context.Context, db *ctxdb.Store, o *runOptions) (sid, cwd, model string, err error) {
	if o.session != "" {
		cwd, model, err = sessionMeta(ctx, db, o.session)
		return o.session, cwd, model, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", "", "", fmt.Errorf("getwd: %w", err)
	}
	return newestSession(ctx, db, wd)
}

// sessionMeta reads a visible session's cwd and model.
func sessionMeta(ctx context.Context, db *ctxdb.Store, sid string) (cwd, model string, err error) {
	var origin string
	err = db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT s.origin, s.cwd, s.model FROM sessions s
			WHERE s.id = ?1 AND NOT EXISTS (SELECT 1 FROM session_labels l WHERE l.session_id = s.id AND l.key = 'purge_state')`,
			[]any{sid}, &origin, &cwd, &model)
	})
	if err != nil {
		return "", "", fmt.Errorf("session %s: %w", sid, err)
	}
	return cwd, model, nil
}

// newestSession returns the newest visible session created in cwd.
func newestSession(ctx context.Context, db *ctxdb.Store, cwd string) (sid, sessionCwd, model string, err error) {
	err = db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT s.id, s.cwd, s.model FROM sessions s
			WHERE s.cwd = ?1 AND s.id <> '__system__'
			AND NOT EXISTS (SELECT 1 FROM session_labels l WHERE l.session_id = s.id AND l.key = 'purge_state')
			ORDER BY s.created_at DESC, s.id LIMIT 1`, []any{cwd}, &sid, &sessionCwd, &model)
	})
	if err != nil {
		return "", "", "", fmt.Errorf("continue in %s: %w", cwd, err)
	}
	return sid, sessionCwd, model, nil
}
