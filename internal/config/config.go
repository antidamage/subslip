// config.go turns raw KEY=VALUE settings into a typed Config.
//
// Works with: dotenv.go (reads the .env file this merges with the process
// environment). Read by cmd/subslip, which hands the fields to the packages
// under internal/. Every setting is listed in specs/subslip.md section 9.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every SubSlip setting after defaults are applied.
type Config struct {
	UpstreamURL *url.URL
	ListenAddr  string
	BasePath    string
	Passthrough bool // SUBSLIP_MODE=passthrough: a plain reverse proxy

	DBPath        string
	CacheTTL      time.Duration
	CacheRefresh  time.Duration
	MinMixTracks  int
	MaxMixTracks  int
	PrecacheMode  string // true or false ("auto" from v0.1 is read as true)
	PlayDelay     time.Duration
	BackgroundGap time.Duration
	MixRateLimit  time.Duration
	BuildTimeout  time.Duration
	// BackgroundMaxConcurrent caps background builds across all users.
	BackgroundMaxConcurrent int
	// MissWait is how long a miss waits on the upstream before an interim
	// answer is built; 0 turns the interim off. InterimBudget bounds that build.
	MissWait      time.Duration
	InterimBudget time.Duration

	MaxSearchLookups   int
	SupplementDeadline time.Duration

	RequireAuth  bool
	AuthCacheTTL time.Duration

	ListenBrainzEnabled bool
	ListenBrainzAPIURL  string

	StatusToken string
	LogLevel    string
	LogFormat   string
	LogFile     string

	// Warnings lists every value that was invalid and replaced by its default.
	// They are logged at startup and shown by `subslip doctor`.
	Warnings []string
	// EnvFile is the .env path that was read, or "" when none was found.
	EnvFile string
}

// Load reads the .env file at envPath (when it exists), overlays the process
// environment, and builds a Config. It never fails on a bad value; it records
// a warning and uses the default, so a typo cannot stop the proxy starting.
func Load(envPath string) (*Config, error) {
	fileValues, found, err := ReadDotenv(envPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", envPath, err)
	}
	merged := fileValues
	for _, kv := range os.Environ() {
		if key, value, ok := strings.Cut(kv, "="); ok {
			merged[key] = value
		}
	}
	cfg := FromMap(merged)
	if found {
		cfg.EnvFile = envPath
	}
	return cfg, nil
}

// FromMap builds a Config from an explicit key/value map. Tests use it
// directly; Load uses it after merging the file and the environment.
func FromMap(values map[string]string) *Config {
	r := reader{values: values}
	cfg := &Config{
		ListenAddr:              r.listenAddr(),
		BasePath:                strings.TrimRight(r.str("BASE_PATH", ""), "/"),
		Passthrough:             strings.EqualFold(r.str("SUBSLIP_MODE", "normal"), "passthrough"),
		DBPath:                  r.str("DB_PATH", "./data/instant_mix_cache.db"),
		CacheTTL:                r.hours("CACHE_TTL_HOURS", 24),
		CacheRefresh:            r.hours("CACHE_REFRESH_HOURS", 6),
		MinMixTracks:            r.integer("MIN_MIX_TRACKS", 15),
		MaxMixTracks:            r.integer("MAX_MIX_TRACKS", 25),
		PrecacheMode:            r.precacheMode(),
		PlayDelay:               r.seconds("FETCH_ON_PLAY_DELAY_SECONDS", 10),
		BackgroundGap:           r.seconds("REMOTE_STATION_API_QUERY_RATE_LIMIT_SECONDS", 10),
		MixRateLimit:            r.seconds("MIX_RATE_LIMIT_SECONDS", 10),
		BuildTimeout:            r.seconds("BACKGROUND_BUILD_TIMEOUT_SECONDS", 90),
		BackgroundMaxConcurrent: r.integer("BACKGROUND_MAX_CONCURRENT", 2),
		MissWait:                r.fractionalSeconds("MISS_WAIT_SECONDS", 22),
		InterimBudget:           r.fractionalSeconds("INTERIM_BUDGET_SECONDS", 6),
		MaxSearchLookups:        r.integer("MAX_SEARCH_LOOKUPS", 20),
		SupplementDeadline:      r.seconds("SUPPLEMENT_DEADLINE_SECONDS", 20),
		RequireAuth:             r.boolean("REQUIRE_AUTH", true),
		AuthCacheTTL:            r.seconds("AUTH_CACHE_TTL_SECONDS", 60),
		ListenBrainzEnabled:     r.boolean("LISTENBRAINZ_ENABLED", true),
		ListenBrainzAPIURL:      strings.TrimRight(r.str("LISTENBRAINZ_API_URL", "https://api.listenbrainz.org"), "/"),
		StatusToken:             r.str("STATUS_TOKEN", ""),
		LogLevel:                r.choice("LOG_LEVEL", "info", "debug", "info", "warn", "error"),
		LogFormat:               r.choice("LOG_FORMAT", "text", "text", "json"),
		LogFile:                 r.str("LOG_FILE", ""),
	}
	cfg.UpstreamURL = r.upstream()
	if cfg.MaxMixTracks < 1 {
		r.warn("MAX_MIX_TRACKS must be at least 1; using 25")
		cfg.MaxMixTracks = 25
	}
	if cfg.MinMixTracks > cfg.MaxMixTracks {
		r.warn("MIN_MIX_TRACKS is above MAX_MIX_TRACKS; using MAX_MIX_TRACKS")
		cfg.MinMixTracks = cfg.MaxMixTracks
	}
	if cfg.BackgroundMaxConcurrent < 1 {
		r.warn("BACKGROUND_MAX_CONCURRENT must be at least 1; using 2")
		cfg.BackgroundMaxConcurrent = 2
	}
	if cfg.MissWait > 0 && cfg.InterimBudget <= 0 {
		r.warn("INTERIM_BUDGET_SECONDS must be above 0 when MISS_WAIT_SECONDS is set; using 6")
		cfg.InterimBudget = 6 * time.Second
	}
	if cfg.BasePath != "" && !strings.HasPrefix(cfg.BasePath, "/") {
		cfg.BasePath = "/" + cfg.BasePath
	}
	cfg.Warnings = r.warnings
	return cfg
}

// reader looks values up and collects a warning for each one it rejects.
type reader struct {
	values   map[string]string
	warnings []string
}

func (r *reader) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

func (r *reader) str(key, def string) string {
	if v, ok := r.values[key]; ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (r *reader) integer(key string, def int) int {
	raw := r.str(key, "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		r.warn("%s=%q is not a whole number; using %d", key, raw, def)
		return def
	}
	return n
}

func (r *reader) seconds(key string, def int) time.Duration {
	return time.Duration(r.integer(key, def)) * time.Second
}

// fractionalSeconds accepts whole or decimal seconds ("22", "0.5").
func (r *reader) fractionalSeconds(key string, def float64) time.Duration {
	raw := r.str(key, "")
	if raw == "" {
		return time.Duration(def * float64(time.Second))
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f < 0 {
		r.warn("%s=%q is not a number of seconds; using %g", key, raw, def)
		return time.Duration(def * float64(time.Second))
	}
	return time.Duration(f * float64(time.Second))
}

// precacheMode reads PRECACHE_ON_PLAY. v0.1's default "auto" (pre-cache only
// after a user's first Instant Mix) is gone; it is read as true.
func (r *reader) precacheMode() string {
	raw := strings.ToLower(r.str("PRECACHE_ON_PLAY", "true"))
	switch raw {
	case "true", "false":
		return raw
	case "auto":
		r.warn("PRECACHE_ON_PLAY=auto was removed in v0.2; every user is pre-cached (set false to turn it off)")
		return "true"
	}
	r.warn("PRECACHE_ON_PLAY=%q is not true or false; using true", raw)
	return "true"
}

func (r *reader) hours(key string, def int) time.Duration {
	return time.Duration(r.integer(key, def)) * time.Hour
}

func (r *reader) boolean(key string, def bool) bool {
	raw := r.str(key, "")
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		r.warn("%s=%q is not true or false; using %t", key, raw, def)
		return def
	}
	return b
}

func (r *reader) choice(key, def string, allowed ...string) string {
	raw := strings.ToLower(r.str(key, def))
	for _, a := range allowed {
		if raw == a {
			return raw
		}
	}
	r.warn("%s=%q is not one of %v; using %s", key, raw, allowed, def)
	return def
}

// listenAddr accepts LISTEN_ADDR, or the plan's PROXY_HOST/PROXY_PORT pair.
func (r *reader) listenAddr() string {
	if addr := r.str("LISTEN_ADDR", ""); addr != "" {
		return addr
	}
	return r.str("PROXY_HOST", "0.0.0.0") + ":" + r.str("PROXY_PORT", "5276")
}

// upstream accepts UPSTREAM_URL, or OCTO_URL as an alias.
func (r *reader) upstream() *url.URL {
	const def = "http://127.0.0.1:4533"
	raw := r.str("UPSTREAM_URL", r.str("OCTO_URL", def))
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// Never echo the value: it may carry a password in its userinfo.
		r.warn("UPSTREAM_URL is not an http(s) URL with a host (value not shown); using %s", def)
		u, _ = url.Parse(def)
	}
	return u
}
