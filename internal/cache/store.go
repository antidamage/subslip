// Package cache is SubSlip's own SQLite store of finished Instant Mixes.
//
// File map:
//   - store.go  (this file) opening, schema, mix rows.
//   - users.go  stats and purge (mix_users, from v0.1, is kept but unused).
//
// The database is pure Go (modernc.org/sqlite), so the binary builds with
// CGO_ENABLED=0 everywhere. SubSlip only ever opens its own file at DB_PATH;
// it never touches the upstream's database or music. No credential is stored.
//
// Used by internal/server (hits, stores), internal/schedule jobs via server,
// and cmd/subslip (cache stats|purge, doctor).
package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/antidamage/subslip/internal/subsonic"
)

// schemaVersion is stored in PRAGMA user_version.
const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS mixes (
  username      TEXT NOT NULL,
  song_id       TEXT NOT NULL,
  songs_json    TEXT NOT NULL,
  envelope_json TEXT NOT NULL,
  source        TEXT NOT NULL,
  track_count   INTEGER NOT NULL,
  built_at      INTEGER NOT NULL,
  build_ms      INTEGER NOT NULL,
  PRIMARY KEY (username, song_id)
);
CREATE INDEX IF NOT EXISTS mixes_built_at ON mixes(built_at);
CREATE TABLE IF NOT EXISTS mix_users (
  username   TEXT PRIMARY KEY,
  first_seen INTEGER NOT NULL
);`

// ErrMiss means no row exists for the key.
var ErrMiss = errors.New("no cached mix")

// Store is an open cache database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Mix is one finished Instant Mix for (user, song).
type Mix struct {
	User    string
	SongID  string
	Songs   []subsonic.Song
	Meta    subsonic.Meta
	Source  string // "upstream" or "upstream+listenbrainz"
	BuiltAt time.Time
	BuildMS int64
}

// Age is how old the mix is at now.
func (m *Mix) Age(now time.Time) time.Duration { return now.Sub(m.BuiltAt) }

// Open opens (creating if needed) the database at path with WAL journaling and
// a 5 s busy timeout, and applies the schema.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) + "?" + url.Values{
		"_pragma": {"journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)"},
	}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database answers, for status and doctor.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Get returns the mix for (user, song) or ErrMiss. Freshness is the caller's
// decision, so the same row can be served and also queued for a refresh.
func (s *Store) Get(ctx context.Context, user, songID string) (*Mix, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT songs_json, envelope_json, source, built_at, build_ms
		   FROM mixes WHERE username = ? AND song_id = ?`, user, songID)
	var songsJSON, envelopeJSON, source string
	var builtAt, buildMS int64
	if err := row.Scan(&songsJSON, &envelopeJSON, &source, &builtAt, &buildMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMiss
		}
		return nil, err
	}
	mix := &Mix{User: user, SongID: songID, Source: source, BuiltAt: time.UnixMilli(builtAt), BuildMS: buildMS}
	if err := decodeJSON(songsJSON, &mix.Songs); err != nil {
		return nil, fmt.Errorf("decoding songs: %w", err)
	}
	if err := decodeJSON(envelopeJSON, &mix.Meta); err != nil {
		return nil, fmt.Errorf("decoding envelope: %w", err)
	}
	return mix, nil
}

// Put stores or replaces the mix for (mix.User, mix.SongID).
func (s *Store) Put(ctx context.Context, mix *Mix) error {
	songsJSON, err := json.Marshal(mix.Songs)
	if err != nil {
		return err
	}
	envelopeJSON, err := json.Marshal(mix.Meta)
	if err != nil {
		return err
	}
	builtAt := mix.BuiltAt
	if builtAt.IsZero() {
		builtAt = s.now()
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO mixes (username, song_id, songs_json, envelope_json, source, track_count, built_at, build_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (username, song_id) DO UPDATE SET
		   songs_json = excluded.songs_json, envelope_json = excluded.envelope_json,
		   source = excluded.source, track_count = excluded.track_count,
		   built_at = excluded.built_at, build_ms = excluded.build_ms`,
		mix.User, mix.SongID, string(songsJSON), string(envelopeJSON), mix.Source,
		len(mix.Songs), builtAt.UnixMilli(), mix.BuildMS)
	return err
}

// decodeJSON keeps numbers as json.Number so ids and sizes round-trip exactly.
func decodeJSON(text string, into any) error {
	dec := json.NewDecoder(stringsReader(text))
	dec.UseNumber()
	return dec.Decode(into)
}
