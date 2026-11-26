// miss.go answers an Instant Mix that is not in the cache (spec 5.3): it joins
// the (user, song) flight, replies with the upstream's answer when it comes,
// sends a short interim when the upstream is slower than MISS_WAIT_SECONDS,
// and records a client that hangs up while the call carries on.
//
// Works with: instantmix.go (instantMix calls answerMiss; answerStage1Error,
// finishAfterMiss), flights.go (join), internal/mix (Stage1, Interim).
package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/antidamage/subslip/internal/mix"
	"github.com/antidamage/subslip/internal/subsonic"
)

// testHookStage1, when set by a test, runs inside the shared upstream call.
var testHookStage1 func()

func (s *Server) answerMiss(w http.ResponseWriter, r *http.Request, req *subsonic.Request, format, user, id string, count int) {
	arrived := time.Now()
	a := req.Auth
	fl, _ := s.flights.join(user, id, s.cfg.BuildTimeout, func(ctx context.Context) (*mix.Stage1Result, error) {
		if testHookStage1 != nil {
			testHookStage1()
		}
		return s.builder.Stage1(ctx, a, req.Method, id, count)
	}, func(fl *flight) { s.afterRequestFlight(user, id, a, fl) })

	var wait <-chan time.Time
	if s.cfg.MissWait > 0 {
		timer := time.NewTimer(s.cfg.MissWait)
		defer timer.Stop()
		wait = timer.C
	}
	for {
		select {
		case <-fl.done:
			s.answerFlight(w, r, req, format, user, id, count, fl)
			return
		case <-r.Context().Done():
			s.clientLeft(user, id, arrived, fl)
			return
		case <-wait:
			wait = nil
			if s.sendInterim(w, req, format, user, id, count, arrived, fl) {
				return
			}
		}
	}
}

// answerFlight replies with the finished upstream call.
func (s *Server) answerFlight(w http.ResponseWriter, r *http.Request, req *subsonic.Request, format, user, id string, count int, fl *flight) {
	if fl.err != nil {
		s.answerStage1Error(w, r, format, user, id, fl.err)
		return
	}
	stage1 := fl.res
	s.meta.Store(stage1.Meta)
	w.Header().Set(HeaderOutcome, "miss")
	s.rec.Count("miss")
	s.rec.Record("miss", user, id, fmt.Sprintf("upstream %d tracks in %d ms", len(stage1.Songs), stage1.Took.Milliseconds()))
	if err := subsonic.WriteSongs(w, format, stage1.Meta, subsonic.ElementFor(req.Method), truncate(stage1.Songs, count)); err != nil {
		s.log.Debug("writing miss response failed", "error", err)
	}
}

// sendInterim answers with a related stand-in list. It returns false, having
// written nothing, when the upstream answered meanwhile or the list is too
// short; the caller then keeps waiting.
func (s *Server) sendInterim(w http.ResponseWriter, req *subsonic.Request, format, user, id string, count int, arrived time.Time, fl *flight) bool {
	songs := fl.interimSongs(func() []subsonic.Song {
		ctx, cancel := context.WithTimeout(context.Background(), s.cfg.InterimBudget)
		defer cancel()
		out, err := s.builder.Interim(ctx, req.Auth, id)
		if err != nil {
			s.rec.Record("interim-failed", user, id, err.Error())
			s.log.Debug("interim could not be built; waiting on the upstream", "user", user, "song", id, "error", err)
		}
		return out
	})
	if fl.answered() {
		return false
	}
	if len(songs) < mix.MinInterimTracks {
		s.rec.Record("interim-skipped", user, id, fmt.Sprintf("only %d related tracks; waiting on the upstream", len(songs)))
		return false
	}
	fl.markInterimSent()
	w.Header().Set(HeaderOutcome, "interim")
	s.rec.Count("interim")
	s.rec.Record("interim", user, id, fmt.Sprintf("%d related tracks after %d ms; the upstream is still building the mix",
		len(songs), time.Since(arrived).Milliseconds()))
	s.log.Info("upstream slow; sent an interim mix", "user", user, "song", id, "tracks", len(songs))
	if err := subsonic.WriteSongs(w, format, s.lastMeta(), subsonic.ElementFor(req.Method), truncate(songs, count)); err != nil {
		s.log.Debug("writing interim response failed", "error", err)
	}
	return true
}

// clientLeft records a client that hung up while the call was running. The
// call is not cancelled: when it finishes, its result is stored as usual.
func (s *Server) clientLeft(user, id string, arrived time.Time, fl *flight) {
	left := time.Since(arrived)
	s.rec.Count("client-left")
	go func() {
		<-fl.done
		outcome := "and failed: " + errString(fl.err)
		if fl.err == nil {
			outcome = fmt.Sprintf("with %d tracks, stored for the next request", len(fl.res.Songs))
		}
		detail := fmt.Sprintf("client left after %.1f s; the upstream answered after %.1f s %s",
			left.Seconds(), time.Since(arrived).Seconds(), outcome)
		s.rec.Record("client-left", user, id, detail)
		s.log.Info("client left before the upstream answered", "user", user, "song", id, "detail", detail)
	}()
}

// afterRequestFlight runs when a flight a request started has finished. It
// stores the mix even if every client has gone.
func (s *Server) afterRequestFlight(user, id string, a subsonic.Auth, fl *flight) {
	if fl.err != nil {
		return
	}
	if fl.interimWasSent() && len(fl.res.Songs) > 0 {
		s.rec.Count("interim-then-real")
	}
	s.finishAfterMiss(user, id, a, fl.res)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
