// Package doctor checks a SubSlip installation and says what is wrong in
// plain words, for a person or an agent. Spec: specs/subslip.md section 8.
//
// File map:
//   - doctor.go  (this file) the checks against config, cache and upstream.
//   - live.go    checks against the running SubSlip (health, timings, status).
//   - report.go  the Check type and text/JSON output.
//
// Used by cmd/subslip/doctor.go. Credentials come from flags or environment,
// are sent only to the upstream and to the local SubSlip, and are never
// printed; the status token is read from the same settings and never printed.
package doctor

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/antidamage/subslip/internal/cache"
	"github.com/antidamage/subslip/internal/config"
	"github.com/antidamage/subslip/internal/listenbrainz"
	"github.com/antidamage/subslip/internal/subsonic"
	"github.com/antidamage/subslip/internal/upstream"
)

// ClientName is the "c" value doctor sends, so status can tell its own
// requests apart from real clients.
const ClientName = "subslip-doctor"

// Options are doctor's inputs.
type Options struct {
	Config   *config.Config
	User     string
	Password string
	Version  string
}

// Run performs every check in order and returns them.
func Run(ctx context.Context, opts Options) []Check {
	d := &run{opts: opts, cfg: opts.Config, up: upstream.New(opts.Config.UpstreamURL)}
	if opts.User != "" {
		d.auth = subsonic.Auth{User: opts.User, Password: "enc:" + hex.EncodeToString([]byte(opts.Password)), Client: ClientName}
	}
	d.checkConfig()
	d.checkCache(ctx)
	d.checkUpstream(ctx)
	d.checkCredentials(ctx)
	d.checkUpstreamMix(ctx)
	d.checkListenBrainz(ctx)
	d.checkLive(ctx)
	d.add(Info, "client URL", "Enter this as the server address in your Subsonic client: "+d.clientURL(),
		"Keep your current server address saved as well; switch back to it if SubSlip is ever down.")
	return d.checks
}

type run struct {
	opts   Options
	cfg    *config.Config
	up     *upstream.Client
	auth   subsonic.Auth
	checks []Check
	songID string // a real song found during the upstream check
}

func (d *run) add(status Status, name, detail, hint string) {
	d.checks = append(d.checks, Check{Status: status, Name: name, Detail: detail, Hint: hint})
}

func (d *run) checkConfig() {
	if d.cfg.EnvFile == "" {
		d.add(Warn, "config", "no .env file found; using environment variables and defaults",
			"Copy .env.example to .env next to the binary, or pass --env.")
	} else {
		d.add(Pass, "config", "read "+d.cfg.EnvFile, "")
	}
	for _, w := range d.cfg.Warnings {
		d.add(Warn, "config value", w, "Fix the value in .env; SubSlip is using the default meanwhile.")
	}
	if d.cfg.Passthrough {
		d.add(Warn, "mode", "SUBSLIP_MODE=passthrough: SubSlip is a plain proxy and caches nothing", "")
	}
}

func (d *run) checkCache(ctx context.Context) {
	store, err := cache.Open(ctx, d.cfg.DBPath)
	if err != nil {
		d.add(Fail, "cache database", fmt.Sprintf("cannot open %s: %v", d.cfg.DBPath, err),
			"SubSlip still proxies everything, but caches nothing. Make DB_PATH's folder writable by the service user.")
		return
	}
	defer store.Close()
	stats, err := store.Stats(ctx, d.cfg.CacheTTL)
	if err != nil {
		d.add(Fail, "cache database", "opened but cannot be read: "+err.Error(), "Run `subslip cache purge`, or delete the file while the service is stopped.")
		return
	}
	d.add(Pass, "cache database", fmt.Sprintf("%s: %d mixes (%d fresh), %d pre-cache users",
		d.cfg.DBPath, stats.Mixes, stats.FreshMixes, stats.Users), "")
}

func (d *run) checkUpstream(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	id := d.up.Detect(ctx)
	if id.Kind == upstream.KindUnreachable {
		d.add(Fail, "upstream", "cannot reach "+d.cfg.UpstreamURL.Redacted(),
			"Check UPSTREAM_URL: it is the address your client used before SubSlip, including any base path such as /navidrome.")
		return
	}
	detail := fmt.Sprintf("%s reachable: kind=%s", d.cfg.UpstreamURL.Redacted(), id.Kind)
	if id.ServerVersion != "" {
		detail += fmt.Sprintf(" (%s %s)", id.Type, id.ServerVersion)
	}
	d.add(Pass, "upstream", detail, "")
}

func (d *run) checkCredentials(ctx context.Context) {
	if d.auth.User == "" {
		d.add(Skip, "credentials", "no --user/--password (or SUBSLIP_DOCTOR_USER/SUBSLIP_DOCTOR_PASSWORD) given",
			"Give a Subsonic login to test Instant Mix end to end.")
		return
	}
	if _, err := d.up.Call(ctx, "ping", d.auth, nil); err != nil {
		if upstream.IsAuthFailure(err) {
			d.add(Fail, "credentials", "the upstream rejected the login for "+d.auth.User, "Check the username and password.")
		} else {
			d.add(Fail, "credentials", "ping failed: "+err.Error(), "")
		}
		d.auth.User = ""
		return
	}
	d.add(Pass, "credentials", "upstream accepted the login for "+d.auth.User, "")
}

func (d *run) checkUpstreamMix(ctx context.Context) {
	if d.auth.User == "" {
		return
	}
	resp, err := d.up.Call(ctx, "getRandomSongs", d.auth, url.Values{"size": {"1"}})
	songs := []subsonic.Song(nil)
	if err == nil {
		songs = upstream.Songs(resp.Body, "randomSongs")
	}
	if len(songs) == 0 {
		d.add(Warn, "library", "getRandomSongs returned no song", "Is the library empty or still scanning?")
		return
	}
	d.songID = upstream.Str(songs[0], "id")
	started := time.Now()
	resp, err = d.up.Call(ctx, "getSimilarSongs2", d.auth, url.Values{"id": {d.songID}, "count": {fmt.Sprint(d.cfg.MaxMixTracks)}})
	if err != nil {
		d.add(Fail, "upstream Instant Mix", "getSimilarSongs2 failed: "+err.Error(), "")
		return
	}
	n := len(upstream.Songs(resp.Body, "similarSongs2"))
	detail := fmt.Sprintf("getSimilarSongs2 on a random song: %d tracks in %d ms (direct to upstream)", n, time.Since(started).Milliseconds())
	switch {
	case n == 0:
		d.add(Warn, "upstream Instant Mix", detail,
			"The upstream returns no similar songs. In Navidrome, set Agents (for example \"lastfm,deezer,listenbrainz\"); SubSlip's ListenBrainz supplement fills in when artists have MusicBrainz ids.")
	case n < d.cfg.MinMixTracks:
		d.add(Pass, "upstream Instant Mix", detail+"; short mixes get the ListenBrainz supplement", "")
	default:
		d.add(Pass, "upstream Instant Mix", detail, "")
	}
}

func (d *run) checkListenBrainz(ctx context.Context) {
	if !d.cfg.ListenBrainzEnabled {
		d.add(Skip, "ListenBrainz", "LISTENBRAINZ_ENABLED=false", "")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := listenbrainz.New(d.cfg.ListenBrainzAPIURL, "SubSlip-doctor/"+d.opts.Version).Reachable(ctx); err != nil {
		d.add(Warn, "ListenBrainz", "not reachable: "+err.Error(), "Short mixes will not be supplemented. Nothing else is affected.")
		return
	}
	d.add(Pass, "ListenBrainz", d.cfg.ListenBrainzAPIURL+" reachable", "")
}

// localBase is the URL doctor uses to reach the running SubSlip.
func (d *run) localBase() string {
	host, port, err := net.SplitHostPort(d.cfg.ListenAddr)
	if err != nil {
		host, port = "127.0.0.1", "5276"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + d.cfg.BasePath
}

// clientURL is the address a client on another machine would use.
func (d *run) clientURL() string {
	host, port, err := net.SplitHostPort(d.cfg.ListenAddr)
	if err != nil {
		return d.localBase()
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		if name, err := os.Hostname(); err == nil {
			host = name
		}
	}
	return "http://" + net.JoinHostPort(host, port) + d.cfg.BasePath
}
