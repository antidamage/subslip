# SubSlip specification

Produced by ticket 080837 from the plan `instant-mix-plan.md` (workspace root),
revised by the idea review on that ticket. This file is the authority for
behaviour; the README describes it for users.

## 1. What SubSlip is

A single Go binary that sits between Subsonic clients and one Subsonic server
(the *upstream*). Every request is proxied unchanged. Two kinds of request get
extra handling:

1. **Playback** (`stream`, `scrobble` with `submission=false`): after proxying,
   schedule a background build of the Instant Mix for that song.
2. **Instant Mix** (`getSimilarSongs`, `getSimilarSongs2`): answer from the
   cache when a fresh mix exists; otherwise build one in the foreground.

The upstream can be **Octo** or **plain Navidrome**, or any Subsonic server
that implements `getSimilarSongs2` and `search3`. Nothing requires Octo.

## 2. Hard rule: SubSlip is never the broken link

SubSlip is installed by people who may not use Instant Mix at all. Every
failure inside SubSlip degrades to plain proxying:

| Failure | Behaviour |
| --- | --- |
| Cache DB cannot open or is corrupt | Start in pass-through mode, log at ERROR, report in `/subslip/status` and `doctor`. |
| Cache read/write error | Treat as a miss / skip the write; proxy upstream. |
| Panic in any SubSlip handler | Recovered; the original request is proxied upstream. |
| Mix build error or timeout | The original Instant Mix request is proxied upstream. |
| Build produces zero tracks | Proxy upstream; nothing is cached. |
| ListenBrainz unreachable | Use what upstream returned; no error to the client. |
| Upstream unreachable | Same answer the client would get without SubSlip (502 from the proxy). |
| `SUBSLIP_MODE=passthrough` | No hooks, no cache, no outbound calls: a plain reverse proxy. |

A request body is never consumed by SubSlip before it is proxied. Hooks read
query parameters only (and the form body for POST, which is buffered and
restored).

Every response carries `X-SubSlip: <outcome>`:
`passthrough`, `hit`, `miss` (answered from SubSlip's own upstream call),
`busy` (flood protection, proxied), `fallback:<code>` and `auth-failed`.
`<code>` is a fixed word from a short list (`panic`, `cache`, `request`,
`format`), never an error string, because error strings can carry URLs.

## 3. Request routing

- Paths ending in `/rest/<method>` or `/rest/<method>.view` are Subsonic calls.
  Method matching is case-insensitive.
- `UPSTREAM_URL` may include a path (`http://host:4533/navidrome`); the
  incoming path is appended to it. `BASE_PATH` (default empty) is stripped from
  incoming paths first, so SubSlip can itself live under a prefix.
- Parameters come from the query string, and for
  `application/x-www-form-urlencoded` POST (OpenSubsonic `formPost`) from the
  body as well.
- `/subslip/*` is SubSlip's own surface (section 8). Everything else, including
  the web UI of the upstream, is proxied.
- Reverse proxy: `httputil.ReverseProxy`, `FlushInterval: -1`, Host header
  rewritten to the upstream, `X-Forwarded-For/Proto/Host` set. Range, 206,
  Content-Range, Accept-Ranges and Content-Length are left untouched.

## 4. Background pre-caching (playback hook)

Defaults in brackets; all from environment.

1. The hook fires after the playback request has been handed to the proxy; it
   never delays the stream. User is `u` (default `default`). Auth parameters
   captured: `u t s p v c` and `apiKey` (OpenSubsonic).
2. Repeated requests for the same `(user, song)` that is already debouncing,
   pending or running are ignored (clients send many Range requests).
3. Per-user debounce `FETCH_ON_PLAY_DELAY_SECONDS` [10]: a new song replaces
   the user's timer; a skipped song never makes an outbound call.
4. After the debounce, per-user slots: one `active`, one `pending`. A new song
   overwrites `pending`. When `active` finishes, `pending` starts no sooner than
   `REMOTE_STATION_API_QUERY_RATE_LIMIT_SECONDS` [10] after the previous start.
5. A song with a fresh cached mix is skipped without any outbound call.
6. Background builds use the captured credentials and have a deadline of
   `BACKGROUND_BUILD_TIMEOUT_SECONDS` [90].
7. Background pre-caching can be turned off alone with `PRECACHE_ON_PLAY=false`.

## 5. Instant Mix requests

1. Auth gate (section 7) runs first when `REQUIRE_AUTH=true`.
2. **Hit:** a fresh entry (`CACHE_TTL_HOURS` [24]) for `(user, song)` is
   returned in the client's format: `similarSongs2` for `getSimilarSongs2`,
   `similarSongs` for `getSimilarSongs`, XML unless `f=json`. `count`
   (default 50 per Subsonic) truncates. `f=jsonp` and unknown formats are
   proxied. Mixes are keyed per user, because Navidrome libraries and
   permissions can differ per user; user B never receives user A's entry.
3. **Miss.** Rule: a miss costs the user no more than calling upstream
   directly, and SubSlip never issues the client's request twice.
   - Any pending debounce for that `(user, song)` is cancelled.
   - SubSlip makes the client's call itself, once: upstream
     `getSimilarSongs2` with the client's `id` and credentials, `f=json`,
     `count` = the larger of the client's count and `MAX_MIX_TRACKS`. That
     answer is the reply, rendered in the client's format, as soon as it
     arrives. There is no SubSlip timeout on it beyond the client's own
     connection; a client that disconnects cancels it.
   - An upstream `status="failed"` answer is passed back as that same Subsonic
     error in the client's format. An upstream HTTP error or network failure is
     answered as the plain proxy would (status code and body, or 502).
   - Only a SubSlip-side failure before that call is sent (a panic, a bad
     request it cannot build) falls back to proxying the original request.
     The call asks for `count` = the larger of the client's `count` (default
     50) and `MAX_MIX_TRACKS` (25), so with default settings it is exactly the
     client's own call.
   - After replying: when stage 1 returned at least `MIN_MIX_TRACKS`, that list
     is the finished mix and is stored at once. When it returned fewer, the
     supplement (section 6) runs in the background under the per-user
     background slots, with no further `getSimilarSongs2` call, and the
     finished mix is stored. A short stage-1 list is never stored on its own.
   - The finished mix keeps stage 1's tracks first and in upstream's order;
     the supplement only appends. Two taps on the same seed therefore show the
     same opening tracks.
   - Worst case, measured on 2026-11-27: Octo cold 1.7-3.5 s, Navidrome
     (AudioMuse-AI agent) cold 0.13-2.1 s. A SubSlip miss is that plus the
     render, a few milliseconds.
4. Flood protection, `MIX_RATE_LIMIT_SECONDS` [10]: no request ever waits on
   SubSlip. While a user's previous Instant Mix call for a different song is
   still in flight, a new one is proxied upstream unchanged (exactly what the
   client would get without SubSlip) and no extra build is started for it.
   Two requests for the same `(user, song)` share one upstream call. No
   request is ever answered with an empty list.
5. `getRandomSongs`, `getAlbumList2` and every other method pass through.
6. Freshness. A hit on an entry older than `CACHE_REFRESH_HOURS` [6] is served
   and a rebuild for that `(user, song)` is queued in the background slots
   (stale-while-revalidate). Entries older than `CACHE_TTL_HOURS` [24] are
   never served. `subslip cache purge` drops everything at once. SubSlip keeps
   no credentials, so it cannot watch the upstream's library scans; a song
   deleted upstream can stay in a mix for up to `CACHE_REFRESH_HOURS` plus one
   rebuild.
7. What a hit bypasses: the upstream never sees the request, so its own
   per-request behaviour (Octo's short repeat cache, plugin logging such as
   AudioMuse-AI's, any per-request filtering by rating or starred state) does
   not run. The order is the one upstream gave when the entry was built; hits
   are not reshuffled, because upstream's order is its similarity ranking.
   A hit carries `X-SubSlip: hit;age=<seconds>`.

### 5.1 Zero cost for people who do not use Instant Mix

`PRECACHE_ON_PLAY` [`auto`]: in `auto`, a user's plays are only pre-cached
after that user has made at least one `getSimilarSongs`/`getSimilarSongs2`
request through SubSlip (recorded by username in the cache DB). Until then
SubSlip adds no upstream or outbound call to their traffic. `true` pre-caches
every user from the start; `false` never does. The flag is persisted in
`mix_users`, so a restart does not put users back to cold. A user's first
Instant Mix is therefore always a miss; the README says so, and `doctor` and
`/subslip/status` show "pre-cache active for <user> since <time>".

### 5.2 Request shapes accepted

No client request capture was available: Octo and Navidrome do not log request
lines at their running log levels. SubSlip therefore logs, at info, the shape
of every Instant Mix request it sees (method, `f`, `count`, auth style
`token`/`password`/`apiKey`, `c`), never the values, so the first real client
leaves a record. Tests cover the variations clients use: `.view` or not, GET
query or form POST, `f` missing/`xml`/`json`/`jsonp`, `count` missing or set,
auth by `u`+`t`+`s`, `u`+`p` (plain and `enc:`), and `apiKey`. Compatibility is
claimed only as "the same response shapes the upstream produces".

## 6. Mix builder

Input: song id and the user's auth parameters. Output: an ordered list of
Subsonic song objects (as upstream JSON), seed excluded, deduplicated by id.

1. `getSimilarSongs2?id=<seed>&count=MAX_MIX_TRACKS [25]` from upstream with
   `f=json`. Octo does its own radio here; Navidrome uses its agents.
   Whatever answers that call upstream (Octo's radio, Navidrome's agents such
   as AudioMuse-AI, Last.fm or Deezer) keeps precedence: its tracks come first
   and in its order. SubSlip only adds tracks when that answer is short.
2. If fewer than `MIN_MIX_TRACKS` [15] and `LISTENBRAINZ_ENABLED` [true]:
   1. `getSong` for the seed (title, artist, `artistId`), then `getArtist` for
      the artist MBID. No artist MBID: no supplement.
   2. Candidates from
      `<LISTENBRAINZ_API_URL>/1/lb-radio/artist/<artist_mbid>?mode=easy&max_similar_artists=10&max_recordings_per_artist=3&pop_begin=0&pop_end=100`,
      most-listened first, with titles from
      `<LISTENBRAINZ_API_URL>/1/metadata/recording/?recording_mbids=...&inc=artist`
      (batched). ListenBrainz Labs similar-recordings is not used: the plan's
      algorithm returned HTTP 400 on 2026-11-27.
   3. Each candidate is resolved with upstream `search3`
      (`query="<artist> <title>"`, `songCount=1`, `artistCount=0`,
      `albumCount=0`), three at a time, at most `MAX_SEARCH_LOOKUPS` [20] per
      build and within `SUPPLEMENT_DEADLINE_SECONDS` [20]. Only results whose
      artist and title match the candidate (case- and punctuation-insensitive
      containment) are kept. A miss is skipped. SubSlip never calls `stream` or
      `download`.
   4. ListenBrainz requests share one client: `User-Agent: SubSlip/<version>`,
      10 s timeout, and at most one request per second.
3. Upstream songs first, then supplements; cap at `MAX_MIX_TRACKS`.
4. Zero tracks: nothing is stored and the caller falls back.

## 7. Authentication

- `REQUIRE_AUTH` [true]: before a cache hit is served or any build starts, the
  caller's credentials are checked with upstream `ping?f=json`. `failed` or
  HTTP 401/403 returns that Subsonic error to the client in its format and
  stops. A network error reaching upstream is not an auth failure: the request
  is proxied.
- Proof cache: `sha256(u:t:s:p:apiKey)` -> expiry, `AUTH_CACHE_TTL_SECONDS` [60].
- `REQUIRE_AUTH=false` skips the check (VPN/LAN setups). Upstream still
  authenticates every proxied request.
- Credentials are never logged and never written to disk. Background jobs keep
  them in memory only for the life of the job.
- Redaction: `t`, `s`, `p`, `apiKey` and `token` are replaced with `***` in
  every log line, status event, error string and doctor output, including URLs
  inside wrapped Go errors and the reverse proxy's error log. A test sends a
  known password through every path and asserts it appears in none of them.

## 8. Observability

- Logs: `log/slog`, `LOG_LEVEL` [info] (`debug|info|warn|error`),
  `LOG_FORMAT` [text] (`text|json`), to stderr, and also to `LOG_FILE` when set.
  Debug logs every hook decision (debounced, superseded, skipped-cached, built
  n tracks in t ms, fallback reason). Query strings are logged with auth
  parameters redacted.
- `GET /subslip/health`: `200 {"status":"ok"}`, no auth, nothing else. For
  process managers and load balancers.
- `GET /subslip/status`: JSON, only with `Authorization: Bearer <STATUS_TOKEN>`.
  The source address is never trusted: behind a reverse proxy, Docker or a
  tunnel every request can arrive from loopback. With `STATUS_TOKEN` unset the
  endpoint answers 404 and logs a hint once; the install scripts generate a
  token into `.env`. Shows version, mode, uptime, config (no secrets), upstream
  reachability and kind (`octo`, `navidrome`, `subsonic`), cache counts,
  counters by outcome, per-user queue state, clients seen (`c` parameter, last
  request time), last 100 events.
- CLI (same binary):
  - `subslip` / `subslip serve`: run the proxy.
  - `subslip doctor [--user U --password P]`: checks config, cache DB, upstream
    reachability and kind, credentials (when given, or from `SUBSLIP_DOCTOR_USER`
    / `SUBSLIP_DOCTOR_PASSWORD`), `getSimilarSongs2` cold time against upstream
    and against SubSlip on a random song, ListenBrainz reachability, and, from a
    running instance's status, which clients have been seen through SubSlip. It
    prints the URL to enter in a client and, when no client traffic has arrived,
    says the client is probably still pointed at the upstream. Each check is
    PASS/WARN/FAIL with a hint; exit 1 on any FAIL. `--json` for agents.
    It reads `STATUS_TOKEN` from the same `.env`/environment the service uses
    and never prints it. It also lists, per user, "pre-cache active since".
  - `subslip cache stats` and `subslip cache purge [--expired]`.
  - `subslip version`.

## 9. Configuration

Environment variables, optionally from a `.env` file (`--env <path>`, default
`./.env`, then the directory of the binary). OS environment wins over the file.

| Variable | Default |
| --- | --- |
| `UPSTREAM_URL` | `http://127.0.0.1:4533` (`OCTO_URL` accepted as an alias) |
| `LISTEN_ADDR` | `0.0.0.0:5276` (`PROXY_HOST`/`PROXY_PORT` accepted) |
| `BASE_PATH` | empty |
| `SUBSLIP_MODE` | `normal` (`passthrough`) |
| `DB_PATH` | `./data/instant_mix_cache.db` |
| `CACHE_TTL_HOURS` / `CACHE_REFRESH_HOURS` | 24 / 6 |
| `MIN_MIX_TRACKS` / `MAX_MIX_TRACKS` | 15 / 25 |
| `PRECACHE_ON_PLAY` | auto (`true`, `false`) |
| `MAX_SEARCH_LOOKUPS` / `SUPPLEMENT_DEADLINE_SECONDS` | 20 / 20 |
| `FETCH_ON_PLAY_DELAY_SECONDS` | 10 |
| `REMOTE_STATION_API_QUERY_RATE_LIMIT_SECONDS` | 10 |
| `MIX_RATE_LIMIT_SECONDS` | 10 |
| `BACKGROUND_BUILD_TIMEOUT_SECONDS` | 90 |
| `REQUIRE_AUTH` / `AUTH_CACHE_TTL_SECONDS` | true / 60 |
| `LISTENBRAINZ_ENABLED` | true |
| `LISTENBRAINZ_API_URL` | `https://api.listenbrainz.org` |
| `STATUS_TOKEN` | empty |
| `LOG_LEVEL` / `LOG_FORMAT` / `LOG_FILE` | info / text / empty |

Invalid values log a WARN and use the default; the process still starts.

## 10. Cache store

SQLite via `modernc.org/sqlite` (CGO off). `journal_mode=WAL`,
`busy_timeout=5000`. Table `mixes(song_id TEXT PRIMARY KEY, songs_json TEXT,
source TEXT, track_count INT, built_at INT, build_ms INT)` holds the upstream
song objects verbatim (all OpenSubsonic fields kept) and re-wraps them per
request. Table `mix_users(username TEXT PRIMARY KEY, first_seen INT)` drives
`PRECACHE_ON_PLAY=auto`. No credential is ever stored. SubSlip only ever
opens its own DB file; it never touches the upstream's database or music.

## 11. Repository and distribution

- Go module `github.com/antidamage/subslip`, `cmd/subslip`, packages under
  `internal/`. Source files stay under 10 KB; every file opens with a comment
  naming what it does and which files it works with.
- Tests: unit tests per package plus an end-to-end test against a fake
  upstream (`httptest`) covering hit, miss, fallback, auth failure, Range
  pass-through and the debounce/slot rules (with injected clocks).
- Distribution: GitHub release archives for linux, darwin, windows on amd64
  and arm64; `install/install.sh` (Linux systemd or macOS launchd) and
  `install/install.ps1` (Windows scheduled task, hidden) that download the
  release, write `.env` without overwriting an existing one, start the service
  and run `doctor`; multi-stage `Dockerfile` (static binary, CA certs) and
  `docker-compose.example.yml`.
- Agent files: `AGENTS.md` (entry point) and `AGENT_SETUP.md` (runbook). No
  tool-specific instruction files.
- README: what it is, how it fits, install by agent / script / Docker / source,
  configuration table, diagnostics, contributing. Plain register.

## 12. SubSlip down must not mean no music

Fail-open covers errors, not a stopped process. So:

- Installing SubSlip never changes the upstream's own address. The upstream URL
  stays live and is the documented fallback; the README and `AGENT_SETUP.md`
  tell users to keep it saved in their client and to switch clients over only
  after `doctor` passes.
- If SubSlip is down, clients pointed at it get nothing until they are
  switched back to the upstream URL by hand. This is the cost of any proxy and
  is stated on the README's first screen.
- The service is started by the platform's supervisor with restart on failure
  (systemd `Restart=always`, launchd `KeepAlive`, a Windows scheduled task
  re-run every few minutes, Docker `restart: unless-stopped`).

## 13. Clients

Responses follow the Subsonic 1.16.1 / OpenSubsonic shapes the upstream itself
produces (song objects are the upstream's own). `/subslip/status` keeps the
first request shape seen per client name (section 5.2) and `doctor` prints it,
so a user can paste it into an issue.

What is fast: an Instant Mix started from the song that is playing (pre-cached
while it plays), and any seed requested before within the TTL. An Instant Mix
on some other song is as fast as the upstream, and the next one on that song
is a hit. The README says this, and does not describe client behaviour as
"automatic". A user whose upstream already answers quickly gains little and
can run `SUBSLIP_MODE=passthrough`.

Published numbers are ranges labelled "one Octo install, one Navidrome
install", with no artist or track names, hosts or ports. Verified against the live
upstream responses of Octo and Navidrome. Any Subsonic client is expected to
work; none is guaranteed beyond that, since the shapes are the upstream's.

## 14. Delivery order

1. PR 1, core: proxy, cache, builder, scheduler, auth, status, doctor, tests,
   `AGENTS.md`, minimal README.
2. Local proof against the live Octo and Navidrome (section 15, items 2-4).
3. PR 2, distribution: install scripts, Dockerfile, compose example, release
   workflow, `AGENT_SETUP.md`, full README.
4. Publish through the workspace's outbound sync, then a GitHub release.

Nothing machine-specific (paths, hosts, tailnet names, usernames, test
credentials) goes into the repo; the local install keeps its `.env` outside it.
On the owner's machine SubSlip is reachable on the LAN and tailnet only; the
public Funnel route keeps pointing at the upstream until ticket 753583 changes
it, and her clients stay on their current URL until she switches them.

## 15. Done means

1. `go test ./...` and `go vet ./...` pass; `CGO_ENABLED=0` builds for all six
   targets.
2. Live install on the owner's machine in front of Octo: `doctor` passes; a
   simulated play of a real song produces a cache row of 15-25 tracks; a
   following `getSimilarSongs2` answers `X-SubSlip: hit` in under 10 ms at the
   proxy; slskd shows no new downloads; a stream with a Range header returns 206.
3. The same checks pass with SubSlip pointed straight at Navidrome. Cold
   (upstream) and warm (hit) `getSimilarSongs2` times for both upstreams are
   recorded on the ticket and in the README's numbers section.
4. With the cache DB path unwritable, SubSlip still proxies everything.
5. Code reviewed and merged; repository published to GitHub with a release.
