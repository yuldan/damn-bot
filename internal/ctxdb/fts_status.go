package ctxdb

import (
	"context"
	"database/sql"
)

// FTSStatus reports every index; a pre-0018 database reports none.
func (s *Store) FTSStatus(ctx context.Context) ([]FTSStatus, error) {
	if has, err := s.ftsMetaStatusColumn(ctx); err != nil || !has {
		return nil, err
	}
	var out []FTSStatus
	err := s.each(ctx, `SELECT name, norm_version, layout_version, reindexed_to, total, status
		FROM fts_meta ORDER BY name`, nil, func(r *sql.Rows) error {
		var st FTSStatus
		if serr := r.Scan(&st.Name, &st.NormVersion, &st.LayoutVersion, &st.ReindexedTo, &st.Total, &st.Status); serr != nil {
			return wrap(serr)
		}
		out = append(out, st)
		return nil
	})
	return out, err
}

func (s *Store) refreshFTSPartial(ctx context.Context) {
	if !s.ftsPartial.Load() {
		return
	}
	sts, err := s.FTSStatus(ctx)
	if err != nil {
		return
	}
	for _, st := range sts {
		if st.Status != "done" {
			return
		}
	}
	s.ftsPartial.Store(false)
}
