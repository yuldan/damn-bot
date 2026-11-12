//go:build !minimal

package ctxdb

import (
	"context"
	"errors"
)

const ftsLockName = "run/fts.lock"

func (s *Store) startFTSUpgrade(ctx context.Context, recreated bool) {
	lk, err := tryLock(s.root, ftsLockName, true)
	if err != nil {
		if !errors.Is(err, ErrDBBusy) {
			s.noteFTSErr(err)
		}
		s.ftsPartial.Store(errors.Is(err, ErrDBBusy))
		return
	}
	s.ftsLock = lk
	ctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.ftsStop, s.ftsDone = stop, make(chan struct{})
	s.ftsPartial.Store(true)
	go func() {
		defer close(s.ftsDone)
		err := s.upgradeFTSLoop(ctx, recreated)
		s.noteFTSErr(err)
		if err == nil {
			s.ftsPartial.Store(false)
		}
	}()
}

func (s *Store) noteFTSErr(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	s.mu.Lock()
	s.ftsErr = errors.Join(s.ftsErr, err)
	s.bgErr = errors.Join(s.bgErr, err)
	s.mu.Unlock()
}

func (s *Store) upgradeFTSLoop(ctx context.Context, recreated bool) error {
	s.ftsMu.Lock()
	defer s.ftsMu.Unlock()
	specs := ftsSpecs()
	for i := range specs {
		spec := &specs[i]
		st, ok, err := s.ftsMeta(ctx, spec.name)
		if err != nil {
			return err
		}
		resume := ok && !recreated && st.Status == "partial" &&
			st.NormVersion == ftsNormVersion && st.LayoutVersion == s.ftsLayout()
		if !resume {
			if ok && !recreated && st.Status == "done" &&
				st.NormVersion == ftsNormVersion && st.LayoutVersion == s.ftsLayout() {
				continue
			}
			if err := s.resetFTS(ctx, spec); err != nil {
				return err
			}
		}
		if err := s.reindexTable(ctx, spec, s.ftsReindexBatch()); err != nil {
			return err
		}
	}
	return nil
}
