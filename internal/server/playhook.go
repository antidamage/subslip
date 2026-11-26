// playhook.go pre-caches the Instant Mix of the song a user starts playing.
//
// Works with: server.go (calls playHook before proxying stream/scrobble),
// instantmix.go (serveHit queues refreshes through backgroundJob), miss.go
// and flights.go (a build and a request for the same song share one upstream
// call), backoff.go, internal/schedule.
//
// The hook only schedules work; it never delays the stream. Every user is
// pre-cached unless PRECACHE_ON_PLAY=false (spec 4.7, 5.1).
package server

import (
	"context"
	"fmt"

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
	s.rec.SeeClient(req.Auth.Client, "")
	if s.backoff.resting(user) {
		s.log.Debug("play noted; pre-cache resting after failed builds", "user", user, "song", id)
		return
	}
	if s.sched.Play(user, id, s.backgroundJob(user, id, req.Auth, false)) {
		s.log.Debug("play noted; pre-cache debouncing", "user", user, "song", id)
	}
}

// backgroundJob builds and stores the mix for (user, id). Unless force is set
// it first checks the cache and skips a mix that is still fresh enough not to
// need a refresh, which costs no upstream call. It joins any flight already
// running for the song; the flight's starter stores the result.
func (s *Server) backgroundJob(user, id string, a subsonic.Auth, force bool) func(context.Context) {
	return func(ctx context.Context) {
		if !force {
			if entry, err := s.store.Get(ctx, user, id); err == nil && entry.Age(s.now()) < s.cfg.CacheRefresh {
				s.rec.Record("background:skipped-fresh", user, id, "")
				return
			}
		}
		fl, leader := s.flights.join(user, id, s.cfg.BuildTimeout, func(ctx context.Context) (*mix.Stage1Result, error) {
			return s.builder.Stage1(ctx, a, "getsimilarsongs2", id, s.cfg.MaxMixTracks)
		}, nil)
		select {
		case <-fl.done:
		case <-ctx.Done():
			s.backgroundFailed(user, id, ctx.Err())
			return
		}
		if fl.err != nil {
			s.backgroundFailed(user, id, fl.err)
			return
		}
		s.backoff.succeeded(user)
		if !leader {
			s.rec.Record("background:joined", user, id, "a request started this call; it stores the mix")
			return
		}
		s.storeResult(ctx, user, id, s.builder.Finish(ctx, a, id, fl.res))
		if fl.interimWasSent() && len(fl.res.Songs) > 0 {
			s.rec.Count("interim-then-real")
		}
	}
}

func (s *Server) backgroundFailed(user, id string, err error) {
	kind := "background:failed"
	if upstream.IsAuthFailure(err) {
		kind = "background:auth-failed"
	}
	s.rec.Record(kind, user, id, err.Error())
	s.log.Debug("background build failed", "user", user, "song", id, "error", err)
	if s.backoff.failed(user) {
		s.rec.Record("background:resting", user, id, fmt.Sprintf("%d failed builds in a row; pre-cache rests for %s", backoffFailures, backoffRest))
		s.log.Info("pre-cache resting after failed builds", "user", user, "for", backoffRest.String())
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
