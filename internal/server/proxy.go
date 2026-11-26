// proxy.go is the reverse proxy every request ends up in unless SubSlip
// answers it itself.
//
// Works with: server.go (routing), instantmix.go and playhook.go (which call
// passthrough and fallback).
//
// The proxy streams with FlushInterval -1, so audio and Range/206 responses
// pass through without buffering; status, headers (Content-Range,
// Accept-Ranges, Content-Length) and body are the upstream's own.
package server

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/antidamage/subslip/internal/logging"
	"github.com/antidamage/subslip/internal/redact"
)

func newProxy(target *url.URL, logger *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target) // joins target's path (e.g. /navidrome) with the incoming one
			pr.SetXForwarded()
		},
		FlushInterval: -1,
		ErrorLog:      logging.StdLogger(logger, slog.LevelWarn),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the client left; nothing to answer
			}
			logger.Warn("upstream request failed", "path", r.URL.Path, "query", redact.Query(r.URL.RawQuery), "error", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
}

// passthrough proxies r and labels the response with outcome.
func (s *Server) passthrough(w http.ResponseWriter, r *http.Request, outcome string) {
	w.Header().Set(HeaderOutcome, outcome)
	s.rec.Count(outcome)
	s.proxy.ServeHTTP(w, r)
}

// fallback proxies r after SubSlip gave up on it. code is a fixed word
// (panic, cache, request, format, auth-unverified), never an error string.
func (s *Server) fallback(w http.ResponseWriter, r *http.Request, code string) {
	s.rec.Record("fallback", "", "", code+" "+r.URL.Path)
	s.passthrough(w, r, "fallback:"+code)
}
