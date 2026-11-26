// status.go serves /subslip/health and /subslip/status (spec section 8).
//
// Works with: server.go (routing), internal/diag (the recorder),
// internal/schedule (queue snapshot), internal/cache (stats).
//
// Health is open and says only "ok". Status needs STATUS_TOKEN as a bearer
// token. The source address is never trusted: behind a reverse proxy, Docker or
// a tunnel every request can arrive from loopback.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/diag"
	"github.com/antidamage/subslip/internal/schedule"
	"github.com/antidamage/subslip/internal/upstream"
)

var statusHintOnce sync.Once

func (s *Server) health(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// StatusDoc is the /subslip/status document. cmd/subslip's doctor reads it.
type StatusDoc struct {
	Version  string               `json:"version"`
	Mode     string               `json:"mode"`
	Uptime   string               `json:"uptime"`
	Config   map[string]any       `json:"config"`
	Upstream upstream.Identity    `json:"upstream"`
	Cache    CacheStatus          `json:"cache"`
	Precache PrecacheStatus       `json:"precache"`
	Queues   []schedule.UserQueue `json:"queues"`
	Record   diag.Snapshot        `json:"record"`
	Warnings []string             `json:"configWarnings,omitempty"`
}

// CacheStatus reports the store.
type CacheStatus struct {
	Available bool         `json:"available"`
	Error     string       `json:"error,omitempty"`
	Stats     *cache.Stats `json:"stats,omitempty"`
}

// PrecacheStatus reports who is pre-cached and since when.
type PrecacheStatus struct {
	Mode        string               `json:"mode"`
	ActiveSince map[string]time.Time `json:"activeSince"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if s.cfg.StatusToken == "" {
		statusHintOnce.Do(func() {
			s.log.Info("a /subslip/status request was refused: set STATUS_TOKEN to enable it")
		})
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "status is disabled; set STATUS_TOKEN"})
		return
	}
	given, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(given), []byte(s.cfg.StatusToken)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or wrong bearer token"})
		return
	}
	writeJSON(w, http.StatusOK, s.StatusDocument(r.Context()))
}

// StatusDocument builds the status document.
func (s *Server) StatusDocument(ctx context.Context) StatusDoc {
	mode := "normal"
	if s.cfg.Passthrough {
		mode = "passthrough"
	}
	if s.store == nil {
		mode = "passthrough (cache unavailable)"
	}
	doc := StatusDoc{
		Version: s.version, Mode: mode,
		Uptime:   s.now().Sub(s.startedAt).Round(time.Second).String(),
		Config:   s.configSummary(),
		Queues:   s.sched.Snapshot(),
		Record:   s.rec.Snapshot(),
		Warnings: s.cfg.Warnings,
		Precache: PrecacheStatus{Mode: s.cfg.PrecacheMode, ActiveSince: map[string]time.Time{}},
	}
	doc.Upstream, _ = s.identity.Load().(upstream.Identity)
	s.mixUsers.Range(func(k, v any) bool {
		doc.Precache.ActiveSince[k.(string)] = v.(time.Time)
		return true
	})
	if s.store == nil {
		doc.Cache = CacheStatus{Error: s.storeErr}
		return doc
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	stats, err := s.store.Stats(ctx, s.cfg.CacheTTL)
	if err != nil {
		doc.Cache = CacheStatus{Available: true, Error: err.Error()}
		return doc
	}
	doc.Cache = CacheStatus{Available: true, Stats: &stats}
	return doc
}

// configSummary lists the settings that shape behaviour. No secrets.
func (s *Server) configSummary() map[string]any {
	c := s.cfg
	return map[string]any{
		"upstreamURL": c.UpstreamURL.Redacted(), "listenAddr": c.ListenAddr, "basePath": c.BasePath,
		"requireAuth": c.RequireAuth, "precacheOnPlay": c.PrecacheMode,
		"minMixTracks": c.MinMixTracks, "maxMixTracks": c.MaxMixTracks,
		"cacheTTL": c.CacheTTL.String(), "cacheRefresh": c.CacheRefresh.String(),
		"playDelay": c.PlayDelay.String(), "backgroundGap": c.BackgroundGap.String(),
		"mixRateLimit": c.MixRateLimit.String(), "listenBrainz": c.ListenBrainzEnabled,
		"logLevel": c.LogLevel,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(body)
}
