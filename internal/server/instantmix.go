// instantmix.go answers getSimilarSongs and getSimilarSongs2.
//
// Works with: flights.go (one upstream call per song, flood rule),
// playhook.go (backgroundJob, used to finish and refresh mixes),
// internal/mix (Stage1, Finish), internal/cache, internal/auth.
//
// Flow (spec section 5): auth gate, then a fresh cached mix is a hit; a miss
// makes the client's own call once and replies with it, then finishes the mix
// in the background. No request waits on SubSlip, and no request is answered
// with an empty list that the upstream did not send.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/antidamage/subslip/internal/auth"
	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/mix"
	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// testHookStage1, when set by a test, runs inside the shared upstream call.
var testHookStage1 func()

// defaultCount is the Subsonic default for getSimilarSongs(2).
const defaultCount = 50

func (s *Server) instantMix(w http.ResponseWriter, r *http.Request, req *subsonic.Request) {
	format, id, user := req.Format(), req.Params.Get("id"), req.Auth.UserOrDefault()
	shape := requestShape(req)
	s.rec.SeeClient(req.Auth.Client, shape)
	s.log.Info("instant mix request", "shape", shape, "user", user, "song", id)

	switch {
	case id == "":
		s.passthrough(w, r, "passthrough")
		return
	case format != "xml" && format != "json":
		s.fallback(w, r, "format")
		return
	case s.store == nil:
		s.fallback(w, r, "cache")
		return
	}
	if s.cfg.RequireAuth && !s.authorise(w, r, req, format) {
		return
	}
	s.markUser(user)
	s.sched.CancelDebounce(user, id)
	count := parseCount(req.Params.Get("count"))

	if s.serveHit(w, r, req, format, user, id, count) {
		return
	}
	if !s.flights.begin(user, id) {
		s.rec.Record("busy", user, id, "")
		s.passthrough(w, r, "busy")
		return
	}
	defer s.flights.end(user)
	stage1, leader, err := s.flights.do(r.Context(), user, id, func() (*mix.Stage1Result, error) {
		if testHookStage1 != nil {
			testHookStage1()
		}
		return s.builder.Stage1(r.Context(), req.Auth, req.Method, id, count)
	})
	if err != nil {
		s.answerStage1Error(w, r, format, user, id, err)
		return
	}
	s.meta.Store(stage1.Meta)
	w.Header().Set(HeaderOutcome, "miss")
	s.rec.Count("miss")
	s.rec.Record("miss", user, id, fmt.Sprintf("upstream %d tracks in %d ms", len(stage1.Songs), stage1.Took.Milliseconds()))
	if err := subsonic.WriteSongs(w, format, stage1.Meta, subsonic.ElementFor(req.Method), truncate(stage1.Songs, count)); err != nil {
		s.log.Debug("writing miss response failed", "error", err)
	}
	if leader {
		go s.finishAfterMiss(user, id, req.Auth, stage1) // followers share the leader's finish
	}
}

// authorise runs the auth gate. It returns false when it has already answered.
func (s *Server) authorise(w http.ResponseWriter, r *http.Request, req *subsonic.Request, format string) bool {
	verdict, failed := s.gate.Check(r.Context(), req.Auth)
	switch verdict {
	case auth.Rejected:
		w.Header().Set(HeaderOutcome, "auth-failed")
		s.rec.Count("auth-failed")
		s.rec.Record("auth-failed", req.Auth.UserOrDefault(), req.Params.Get("id"), "")
		subsonic.WriteError(w, format, s.lastMeta(), failed.Code, failed.Message)
		return false
	case auth.Unknown:
		s.fallback(w, r, "auth-unverified")
		return false
	}
	return true
}

// serveHit answers from the cache when a fresh entry exists. A hit older than
// CACHE_REFRESH_HOURS also queues a rebuild.
func (s *Server) serveHit(w http.ResponseWriter, r *http.Request, req *subsonic.Request, format, user, id string, count int) bool {
	entry, err := s.store.Get(r.Context(), user, id)
	if err != nil {
		if !errors.Is(err, cache.ErrMiss) {
			s.log.Warn("cache read failed; treating as a miss", "error", err)
			s.rec.Count("cache-error")
		}
		return false
	}
	age := entry.Age(s.now())
	if age >= s.cfg.CacheTTL {
		return false
	}
	w.Header().Set(HeaderOutcome, "hit;age="+strconv.Itoa(int(age.Seconds())))
	s.rec.Count("hit")
	if err := subsonic.WriteSongs(w, format, entry.Meta, subsonic.ElementFor(req.Method), truncate(entry.Songs, count)); err != nil {
		s.log.Debug("writing hit response failed", "error", err)
	}
	if age >= s.cfg.CacheRefresh {
		s.sched.Enqueue(user, id, s.backgroundJob(user, id, req.Auth, true))
	}
	return true
}

// answerStage1Error passes the upstream's own failure back. Only a failure that
// left nothing to pass back (a non-JSON 200) is retried as a plain proxy.
func (s *Server) answerStage1Error(w http.ResponseWriter, r *http.Request, format, user, id string, err error) {
	if r.Context().Err() != nil {
		return // the client went away
	}
	s.rec.Record("upstream-error", user, id, err.Error())
	var failed *upstream.FailedError
	var httpErr *upstream.HTTPError
	switch {
	case errors.Is(err, errFlightPanicked):
		// The request this one shared a call with panicked; ask upstream directly.
		s.fallback(w, r, "panic")
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		// The request this one was sharing a call with went away first.
		s.fallback(w, r, "request")
	case errors.As(err, &failed):
		w.Header().Set(HeaderOutcome, "upstream-error")
		s.rec.Count("upstream-error")
		subsonic.WriteError(w, format, s.lastMeta(), failed.Code, failed.Message)
	case errors.As(err, &httpErr) && httpErr.Status == http.StatusOK:
		s.fallback(w, r, "format")
	case errors.As(err, &httpErr):
		w.Header().Set(HeaderOutcome, "upstream-error")
		s.rec.Count("upstream-error")
		if httpErr.ContentType != "" {
			w.Header().Set("Content-Type", httpErr.ContentType)
		}
		w.WriteHeader(httpErr.Status)
		w.Write(httpErr.Body)
	default:
		s.log.Warn("upstream unreachable for an Instant Mix call", "error", err)
		w.Header().Set(HeaderOutcome, "upstream-error")
		s.rec.Count("upstream-error")
		w.WriteHeader(http.StatusBadGateway)
	}
}

// finishAfterMiss stores the finished mix: at once when stage 1 was long
// enough, otherwise through the background slots so the supplement is rate
// limited with every other build.
func (s *Server) finishAfterMiss(user, id string, a subsonic.Auth, stage1 *mix.Stage1Result) {
	// This runs in its own goroutine, outside the request's recovery: a panic
	// here must not take the proxy down.
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("recovered from a panic while finishing a mix", "user", user, "song", id)
			s.rec.Record("panic", user, id, "finishing a mix")
		}
	}()
	if len(stage1.Songs) >= s.cfg.MinMixTracks || s.builder.LB == nil {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.BuildTimeout)
		defer cancel()
		s.storeResult(ctx, user, id, s.builder.Finish(ctx, a, id, stage1))
		return
	}
	s.sched.Enqueue(user, id, func(ctx context.Context) {
		s.storeResult(ctx, user, id, s.builder.Finish(ctx, a, id, stage1))
	})
}

// lastMeta is the envelope of the most recent upstream answer, so SubSlip's
// own error replies carry the upstream's version and type fields.
func (s *Server) lastMeta() subsonic.Meta {
	if meta, ok := s.meta.Load().(subsonic.Meta); ok {
		return meta
	}
	return subsonic.DefaultMeta()
}

// requestShape describes a request without any values, for logs and status.
func requestShape(req *subsonic.Request) string {
	count := req.Params.Get("count")
	if count == "" {
		count = "-"
	}
	return fmt.Sprintf("%s f=%s count=%s auth=%s v=%s", req.Method, req.Format(), count, req.Auth.Style(), req.Auth.Version)
}

func parseCount(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultCount
	}
	return min(n, 500)
}

func truncate(songs []subsonic.Song, count int) []subsonic.Song {
	if len(songs) > count {
		return songs[:count]
	}
	return songs
}
