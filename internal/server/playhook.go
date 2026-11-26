// playhook.go pre-caches the Instant Mix of the song a user starts playing.
//
// Works with: server.go (calls playHook before proxying stream/scrobble),
// instantmix.go (reuses backgroundJob and storeResult), internal/schedule.
//
// The hook only schedules work; it never delays the stream. With
// PRECACHE_ON_PLAY=auto it does nothing for a user until that user has made an
// Instant Mix request through SubSlip (spec 5.1).
package server

import (
	"context"
	"fmt"
	"time"

	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/mix"
	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

func (s *Server) playHook(req *subsonic.Request) {
	if s.store == nil || s.cfg.PrecacheMode == "false" {
		return
	}
	if req.Method == "scrobble" && req.Params.Get("submission") != "false" {
		return // only "now playing" scrobbles mark a song as started
	}
	id := req.Params.Get("id")
	if id == "" {
		return
	}
	user := req.Auth.UserOrDefault()
	if s.cfg.PrecacheMode == "auto" && !s.knownUser(user) {
		return
	}
	s.rec.SeeClient(req.Auth.Client, "")
	if s.sched.Play(user, id, s.backgroundJob(user, id, req.Auth, false)) {
		s.log.Debug("play noted; pre-cache debouncing", "user", user, "song", id)
	}
}

// backgroundJob builds and stores the mix for (user, id). Unless force is set
// it first checks the cache and skips a mix that is still fresh enough not to
// need a refresh, which costs no upstream call.
func (s *Server) backgroundJob(user, id string, a subsonic.Auth, force bool) func(context.Context) {
	return func(ctx context.Context) {
		if !force {
			if entry, err := s.store.Get(ctx, user, id); err == nil && entry.Age(s.now()) < s.cfg.CacheRefresh {
				s.rec.Record("background:skipped-fresh", user, id, "")
				return
			}
		}
		result, err := s.builder.Build(ctx, a, id)
		if err != nil {
			kind := "background:failed"
			if upstream.IsAuthFailure(err) {
				kind = "background:auth-failed"
			}
			s.rec.Record(kind, user, id, err.Error())
			s.log.Debug("background build failed", "user", user, "song", id, "error", err)
			return
		}
		s.storeResult(ctx, user, id, result)
	}
}

// storeResult writes a finished mix. An empty mix is not stored, so the next
// request asks the upstream again rather than getting nothing from the cache.
func (s *Server) storeResult(ctx context.Context, user, id string, result *mix.Result) {
	if len(result.Songs) == 0 {
		s.rec.Record("empty", user, id, result.Note)
		s.log.Info("no mix to store: upstream returned no similar songs and the supplement found none",
			"user", user, "song", id, "reason", result.Note)
		return
	}
	err := s.store.Put(ctx, &cache.Mix{
		User: user, SongID: id, Songs: result.Songs, Meta: result.Meta,
		Source: result.Source, BuiltAt: s.now(), BuildMS: result.Took.Milliseconds(),
	})
	if err != nil {
		s.log.Warn("could not store a finished mix", "user", user, "song", id, "error", err)
		s.rec.Count("cache-error")
		return
	}
	s.meta.Store(result.Meta)
	detail := fmt.Sprintf("%d tracks (%d supplemented) from %s in %d ms",
		len(result.Songs), result.Supplemented, result.Source, result.Took.Milliseconds())
	if result.Note != "" {
		detail += "; " + result.Note
	}
	s.rec.Record("stored", user, id, detail)
	s.log.Info("mix stored", "user", user, "song", id, "tracks", len(result.Songs),
		"supplemented", result.Supplemented, "source", result.Source, "ms", result.Took.Milliseconds())
}

// markUser records an Instant Mix user for PRECACHE_ON_PLAY=auto.
func (s *Server) markUser(user string) {
	if _, known := s.mixUsers.Load(user); known {
		return
	}
	s.mixUsers.Store(user, s.now())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.store.MarkUser(ctx, user); err != nil {
		s.log.Warn("could not record a pre-cache user; it lasts until restart", "user", user, "error", err)
		return
	}
	s.log.Info("pre-cache active for user", "user", user, "mode", s.cfg.PrecacheMode)
}

func (s *Server) knownUser(user string) bool {
	_, ok := s.mixUsers.Load(user)
	return ok
}
