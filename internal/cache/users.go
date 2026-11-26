// users.go holds cache statistics and purging.
//
// Works with: store.go (the open database). The mix_users table that v0.1's
// PRECACHE_ON_PLAY=auto used is still created by store.go, so a downgrade
// works, but nothing reads or writes it any more (spec section 10).
package cache

import (
	"context"
	"strings"
	"time"
)

// Stats summarises the cache for status and `subslip cache stats`.
type Stats struct {
	Mixes       int        `json:"mixes"`
	FreshMixes  int        `json:"freshMixes"`
	Tracks      int        `json:"tracks"`
	Users       int        `json:"users"` // users with at least one cached mix
	OldestBuilt *time.Time `json:"oldestBuilt,omitempty"`
	NewestBuilt *time.Time `json:"newestBuilt,omitempty"`
}

// Stats counts rows; fresh means built within ttl.
func (s *Store) Stats(ctx context.Context, ttl time.Duration) (Stats, error) {
	var st Stats
	var oldest, newest *int64
	cutoff := s.now().Add(-ttl).UnixMilli()
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(built_at >= ?), 0), COALESCE(SUM(track_count), 0),
		        MIN(built_at), MAX(built_at) FROM mixes`, cutoff).
		Scan(&st.Mixes, &st.FreshMixes, &st.Tracks, &oldest, &newest)
	if err != nil {
		return st, err
	}
	if oldest != nil {
		t := time.UnixMilli(*oldest)
		st.OldestBuilt = &t
	}
	if newest != nil {
		t := time.UnixMilli(*newest)
		st.NewestBuilt = &t
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT username) FROM mixes`).Scan(&st.Users)
	return st, err
}

// Purge deletes mixes: with onlyExpired those older than ttl, otherwise all
// of them (and the unused mix_users table).
func (s *Store) Purge(ctx context.Context, onlyExpired bool, ttl time.Duration) (int64, error) {
	if onlyExpired {
		res, err := s.db.ExecContext(ctx, `DELETE FROM mixes WHERE built_at < ?`, s.now().Add(-ttl).UnixMilli())
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM mixes`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	_, err = s.db.ExecContext(ctx, `DELETE FROM mix_users`)
	return n, err
}

func stringsReader(text string) *strings.Reader { return strings.NewReader(text) }
