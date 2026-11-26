// users.go holds the mix_users table, cache statistics and purging.
//
// Works with: store.go (the open database). mix_users records, by username
// only, who has made an Instant Mix request through SubSlip. With
// PRECACHE_ON_PLAY=auto only those users' plays are pre-cached (spec 5.1).
package cache

import (
	"context"
	"strings"
	"time"
)

// MarkUser records that user has asked for an Instant Mix. It keeps the first
// time it was seen.
func (s *Store) MarkUser(ctx context.Context, user string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mix_users (username, first_seen) VALUES (?, ?)
		 ON CONFLICT (username) DO NOTHING`, user, s.now().UnixMilli())
	return err
}

// Users returns every recorded user and when each was first seen.
func (s *Store) Users(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username, first_seen FROM mix_users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := map[string]time.Time{}
	for rows.Next() {
		var name string
		var first int64
		if err := rows.Scan(&name, &first); err != nil {
			return nil, err
		}
		users[name] = time.UnixMilli(first)
	}
	return users, rows.Err()
}

// Stats summarises the cache for status and `subslip cache stats`.
type Stats struct {
	Mixes       int        `json:"mixes"`
	FreshMixes  int        `json:"freshMixes"`
	Tracks      int        `json:"tracks"`
	Users       int        `json:"users"`
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
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mix_users`).Scan(&st.Users)
	return st, err
}

// Purge deletes mixes. With onlyExpired it deletes those older than ttl and
// keeps mix_users; otherwise it empties both tables.
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
