// Package server is SubSlip's HTTP front: it proxies every request to the
// upstream and adds the two hooks described in specs/subslip.md.
//
// File map:
//   - server.go      (this file) wiring, routing, panic recovery, the
//     upstream identity refresher.
//   - proxy.go       the reverse proxy and the pass-through/fallback helpers.
//   - instantmix.go  getSimilarSongs / getSimilarSongs2: hit, errors.
//   - miss.go        a miss: the detached upstream call, the interim, and a
//     client that hangs up.
//   - flights.go     shares one upstream call per (user, song) and the
//     per-user flood rule.
//   - playhook.go    stream / scrobble: schedules background builds.
//   - backoff.go     rests a user's pre-cache after failed builds.
//   - status.go      /subslip/health and /subslip/status.
//
// Hard rule (spec section 2): anything SubSlip cannot do degrades to plain
// proxying. Every hooked path runs under recoverTo, which proxies the original
// request if SubSlip panics before writing a response.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/antidamage/subslip/internal/auth"
	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/config"
	"github.com/antidamage/subslip/internal/diag"
	"github.com/antidamage/subslip/internal/listenbrainz"
	"github.com/antidamage/subslip/internal/mix"
	"github.com/antidamage/subslip/internal/schedule"
	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// HeaderOutcome is the response header naming what SubSlip did.
const HeaderOutcome = "X-SubSlip"

// Options are what New needs. Store may be nil: SubSlip then runs every
// request as a pass-through and reports StoreError.
type Options struct {
	Config     *config.Config
	Logger     *slog.Logger
	Store      *cache.Store
	StoreError string
	Version    string
	Clock      schedule.Clock
}

// Server is the http.Handler.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	version  string
	store    *cache.Store
	storeErr string

	proxy   http.Handler
	up      *upstream.Client
	builder *mix.Builder
	gate    *auth.Gate
	sched   *schedule.Scheduler
	rec     *diag.Recorder
	flights *flights
	backoff *backoff
	now     func() time.Time

	identity  atomic.Value // upstream.Identity
	meta      atomic.Value // subsonic.Meta of the latest upstream answer
	startedAt time.Time
}

// New wires a Server.
func New(opts Options) *Server {
	cfg := opts.Config
	clock := opts.Clock
	if clock == nil {
		clock = schedule.RealClock{}
	}
	s := &Server{
		cfg: cfg, log: opts.Logger, version: opts.Version,
		store: opts.Store, storeErr: opts.StoreError,
		up:  upstream.New(cfg.UpstreamURL),
		rec: diag.NewRecorder(), now: clock.Now, startedAt: clock.Now(),
	}
	s.proxy = newProxy(cfg.UpstreamURL, opts.Logger)
	s.gate = auth.New(s.up, cfg.AuthCacheTTL)
	s.flights = newFlights(cfg.MixRateLimit, clock.Now)
	s.flights.panicked = func(where string, value any) {
		s.log.Error("recovered from a panic in a detached Instant Mix call", "where", where, "panic", slog.AnyValue(value).String())
		s.rec.Record("panic", "", "", where)
	}
	s.backoff = newBackoff(clock.Now)
	s.builder = &mix.Builder{
		Up: s.up, MinTracks: cfg.MinMixTracks, MaxTracks: cfg.MaxMixTracks,
		MaxSearchLookups: cfg.MaxSearchLookups, SupplementDeadline: cfg.SupplementDeadline, Log: opts.Logger,
	}
	if cfg.ListenBrainzEnabled {
		s.builder.LB = listenbrainz.New(cfg.ListenBrainzAPIURL, "SubSlip/"+opts.Version+" (+https://github.com/antidamage/subslip)")
	}
	s.sched = schedule.New(clock, cfg.PlayDelay, cfg.BackgroundGap, cfg.BuildTimeout, s.schedulerEvent)
	s.sched.SetMaxConcurrent(cfg.BackgroundMaxConcurrent)
	s.identity.Store(upstream.Identity{Kind: "unknown"})
	return s
}

// Run refreshes the upstream identity every ten minutes until ctx ends. In
// passthrough mode it does nothing: that mode makes no calls of its own.
func (s *Server) Run(ctx context.Context) {
	if s.cfg.Passthrough {
		return
	}
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		s.refreshIdentity(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Scheduler exposes the background scheduler, for tests and shutdown.
func (s *Server) Scheduler() *schedule.Scheduler { return s.sched }

// ServeHTTP routes one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.stripBasePath(r)
	switch r.URL.Path {
	case "/subslip/health":
		s.health(w)
		return
	case "/subslip/status":
		s.status(w, r)
		return
	}
	if s.cfg.Passthrough {
		s.passthrough(w, r, "passthrough")
		return
	}
	method := subsonic.MethodFromPath(r.URL.Path)
	switch method {
	case "getsimilarsongs", "getsimilarsongs2", "stream", "scrobble":
	default:
		s.passthrough(w, r, "passthrough")
		return
	}
	tw := &trackingWriter{ResponseWriter: w}
	defer s.recoverTo(tw, r)
	req, ok := subsonic.Parse(r)
	if !ok {
		s.fallback(tw, r, "request")
		return
	}
	if method == "stream" || method == "scrobble" {
		s.playHook(req)
		s.passthrough(tw, r, "passthrough")
		return
	}
	s.instantMix(tw, r, req)
}

// recoverTo turns a panic in a hooked path into a plain proxied request when
// nothing has been written yet.
func (s *Server) recoverTo(tw *trackingWriter, r *http.Request) {
	p := recover()
	if p == nil {
		return
	}
	if p == http.ErrAbortHandler {
		panic(p) // the client went away; let net/http handle it
	}
	s.log.Error("recovered from a panic; proxying the request unchanged", "path", r.URL.Path, "panic", slog.AnyValue(p).String())
	s.rec.Record("panic", "", "", r.URL.Path)
	if !tw.wrote {
		s.fallback(tw, r, "panic")
	}
}

func (s *Server) stripBasePath(r *http.Request) {
	if s.cfg.BasePath == "" {
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, s.cfg.BasePath); ok && (rest == "" || rest[0] == '/') {
		r.URL.Path = rest
		r.URL.RawPath = ""
	}
}

func (s *Server) refreshIdentity(ctx context.Context) {
	defer func() {
		if recover() != nil {
			s.log.Error("recovered from a panic while identifying the upstream")
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	id := s.up.Detect(ctx)
	previous, _ := s.identity.Load().(upstream.Identity)
	s.identity.Store(id)
	if previous.Kind != id.Kind {
		s.log.Info("upstream identified", "kind", id.Kind, "type", id.Type, "serverVersion", id.ServerVersion, "error", id.Error)
	}
}

func (s *Server) schedulerEvent(user, song, what string) {
	s.rec.Record("background:"+what, user, song, "")
	s.log.Debug("background build", "event", what, "user", user, "song", song)
}

// trackingWriter remembers whether a response has started, so recovery knows
// whether it can still proxy.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) { t.wrote = true; t.ResponseWriter.WriteHeader(code) }
func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController (used by the reverse proxy to flush)
// reach the underlying writer.
func (t *trackingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }
