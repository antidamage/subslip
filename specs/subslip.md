# SubSlip specification

Produced by ticket 080837 from the plan `instant-mix-plan.md` (workspace root),
revised by the idea review on that ticket. Revised for v0.2.0 by ticket 626441
(pre-cache for every user, a miss that survives the client hanging up, and the
interim answer). This file is the authority for behaviour; the README
describes it for users.

## 1. What SubSlip is

A single Go binary that sits between Subsonic clients and one Subsonic server
(the *upstream*). Every request is proxied unchanged. Two kinds of request get
extra handling:

1. **Playback** (`stream`, `scrobble` with `submission=false`): after proxying,
   schedule a background build of the Instant Mix for that song.
2. **Instant Mix** (`getSimilarSongs`, `getSimilarSongs2`): answer from the
   cache when a fresh mix exists. Otherwise, make the upstream call and
   answer with it. If it is too slow, answer with a short interim list
   before the client gives up (section 5.3).

The upstream can be **Octo** or **plain Navidrome**, or any Subsonic server
that implements `getSimilarSongs2` and `search3`. Nothing requires Octo.

## 2. Hard rule: SubSlip is never the broken link

SubSlip may be installed by people who never use Instant Mix. With the
default `PRECACHE_ON_PLAY=true`, each song they play still costs one upstream
Instant Mix call in the background, after the debounce and within the
concurrency cap (section 4). `PRECACHE_ON_PLAY=false` removes that.
Every failure inside SubSlip degrades to plain proxying:

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
`interim` (the upstream was too slow; section 5.3), `busy` (flood
protection: proxied unchanged, and stored when it qualifies; section 5.4),
`fallback:<code>` and `auth-failed`.
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
7. `PRECACHE_ON_PLAY` [true] pre-caches every user's plays. `false` turns
   background pre-caching off on its own. The old value `auto` is read as
   `true`, with a startup warning.
8. At most `BACKGROUND_MAX_CONCURRENT` [2] background builds run at once,
   across all users. Others wait for a free slot, keeping their per-user order.
9. Backoff: after 3 background builds in a row fail or time out for a user,
   that user's pre-caching rests for 10 minutes (event
   `background:resting`). A failing upstream therefore gets no extra load
   from plays. Taps during that time are cold and rely on section 5.3.
10. A background build and an Instant Mix request for the same
   `(user, song)` share one upstream call (section 5.4).
11. Empty retry, `EMPTY_RETRY_SECONDS` [10] (0 turns it off). When a build
   ends with no tracks, one more build runs for that `(user, song)` this long
   later, through the slots and cap. The build can be a background one or the
   finish after a miss.
   - It is skipped while the user is resting (item 9), and when a fresh mix
     exists by then.
   - It runs at most once per song per hour. It never retries itself and
     never counts toward the backoff.
   - It does not change what the request that got the empty answer saw. It
     only helps a later tap.
   - Counters: `empty-retry` and `empty-retry-filled`. When at least 50
     retries have run and fewer than 1 in 10 filled, `doctor` WARNs and the
     retry should be removed or turned off.
   - Reason: on 2026-11-27, Octo answered a seed with 0 tracks, then 50 four
     seconds later. Its YouTube seed search is not cached when the yt-dlp shim
     fails, and the response itself cannot tell that apart from a seed with
     nothing similar.

## 5. Instant Mix requests

1. Auth gate (section 7) runs first when `REQUIRE_AUTH=true`.
2. **Hit:** a fresh entry (`CACHE_TTL_HOURS` [24]) for `(user, song)` is
   returned in the client's format: `similarSongs2` for `getSimilarSongs2`,
   `similarSongs` for `getSimilarSongs`, XML unless `f=json`. `count`
   (default 50 per Subsonic) truncates. `f=jsonp` and unknown formats are
   proxied. Mixes are keyed per user, because Navidrome libraries and
   permissions can differ per user; user B never receives user A's entry.
3. **Miss.** Rule: a miss never makes the user wait longer than calling
   upstream directly, a slow upstream does not turn into a client timeout, and
   SubSlip never issues the client's request twice.
   - Any pending debounce for that `(user, song)` is cancelled.
   - SubSlip makes the client's call itself, once: upstream
     `getSimilarSongs2` with the client's `id` and credentials, `f=json`,
     `count` = the larger of the client's count and `MAX_MIX_TRACKS`. That
     answer is the reply, rendered in the client's format, as soon as it
     arrives.
   - The call does not run on the client's connection. Its deadline is
     `BACKGROUND_BUILD_TIMEOUT_SECONDS` [90]. A client that hangs up does not
     cancel it: the answer is still finished and stored, so the retry is a
     hit. The event `client-left` records when the client went (seconds after
     the request) and when the result arrived. It also bumps the
     `client-left` counter.
   - **Interim.** If the upstream has not answered after `MISS_WAIT_SECONDS`
     [22], SubSlip builds an interim list within `INTERIM_BUDGET_SECONDS` [6]
     using fast library calls:
     - `getSong` for the seed;
     - `getTopSongs` for the seed's artist (count 10);
     - `getRandomSongs` in the seed's genre (size 25).

     Artist tracks and genre tracks are interleaved, artist first. The seed
     is excluded, duplicates are removed, and the list is capped at
     `MAX_MIX_TRACKS`. Nothing unrelated is added. With 5 or more tracks it is
     the reply, carrying `X-SubSlip: interim`. With fewer, or if the budget
     runs out, nothing is sent and the request keeps waiting on the upstream
     call.
     - An interim is never stored and never served as a hit. The upstream
       call keeps running and its result is stored as usual. A client cannot
       replace a queue it already has, so the next tap on that seed is the
       one that gets the real mix.
     - Counters: `interim` (replies sent) and `interim-then-real` (the real
       mix was stored after an interim for the same `(user, song)`).
     - Joiners of one flight share one interim assembly.
     - `MISS_WAIT_SECONDS=0` turns the interim off.
     - Reason: SwiftSonic (Minidisc) gives up after 30 s. On 2026-11-27 Octo
       took 31 s on one seed while its yt-dlp calls to YouTube were timing
       out. 22 s plus the 6 s budget stays under 30 s.
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
   - Measured on 2026-11-27:
     - Octo cold, normal case: 1.7-3.5 s.
     - Octo cold, worst seen: 31 s, with YouTube timing out.
     - Navidrome (AudioMuse-AI agent) cold: 0.13-2.1 s.

     A SubSlip miss is that time plus the render, a few milliseconds.
4. Flood protection, `MIX_RATE_LIMIT_SECONDS` [10]: no request ever waits on
   SubSlip. While a user's previous Instant Mix call for a different song is
   still in flight, a new one is proxied upstream unchanged (exactly what the
   client would get without SubSlip) and no extra build is started for it.
   Requests for the same `(user, song)` share one upstream call. That
   includes requests made after the first client hung up, and a background
   build already running for that song. A flight stays joinable until its
   result is stored, or handed to the background slots for the supplement.
   A request that arrives after the upstream answered, but before the store,
   gets that answer, not a second upstream call.
   - **Busy answers are stored.** The request the flood rule turns away is
     still proxied byte for byte, with the outcome `busy`. SubSlip copies the
     answer as it streams past (at most 2 MB). After the client has it,
     SubSlip stores the copy only when all of these hold:
     - it was HTTP 200 and a Subsonic ok answer in JSON;
     - the client's `count` was at least `MAX_MIX_TRACKS`;
     - at least `MIN_MIX_TRACKS` tracks remain after seed exclusion and
       dedupe;
     - no fresh entry exists for that `(user, song)`.

     A busy copy never overwrites. It is stored with source `upstream-busy`.
     Errors, non-200, XML, other encodings and oversized bodies are relayed
     unchanged and not stored.
   - Each busy request is a real upstream call that the client made anyway,
     so storing it adds no upstream call. Counters: `busy-stored`,
     `busy-skipped`, and `busy-stored-then-hit` (a hit on an entry stored
     this way). SwiftSonic sends `getSimilarSongs2 f=json count=25` for eight
     songs of each mix, and those qualify. The call asks for the largest count
   among the requests present when it started. No request is ever answered
   with an empty list.
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

### 5.1 Pre-cache for every user

Up to v0.1, `PRECACHE_ON_PLAY=auto` (the old default) pre-cached a user's plays
only after their first Instant Mix. That made every user's first Instant Mix
a cold call, which a slow upstream turned into a failure. From v0.2 every
user is pre-cached from their first play.

`mix_users` is no longer read. An existing database opens unchanged and the
table is left in place, so a downgrade still works.

The cost is one background upstream call per song played, after the debounce
and under the cap and backoff (section 4). The README says so on its first
screen and gives `PRECACHE_ON_PLAY=false` as the opt-out.

### 5.2 Request shapes accepted

No client request capture was available: Octo and Navidrome do not log request
lines at their running log levels. SubSlip therefore logs, at info, the shape
of every Instant Mix request it sees (method, `f`, `count`, auth style
`token`/`password`/`apiKey`, `c`), never the values, so the first real client
leaves a record. Tests cover the variations clients use: `.view` or not, GET
query or form POST, `f` missing/`xml`/`json`/`jsonp`, `count` missing or set,
auth by `u`+`t`+`s`, `u`+`p` (plain and `enc:`), and `apiKey`. Compatibility is
claimed only as "the same response shapes the upstream produces".

Captured on 2026-11-27, SwiftSonic (Minidisc), and used as a test fixture:
- `getSimilarSongs`, `f=json`, token auth, `v=1.16.1`;
- it sends `count=50` and `count=100` for the same seed at the same moment;
- about 0.5 s later it sends `getSimilarSongs2 count=25` for eight other songs
  of the result;
- the client gives up after 30 s.

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

SubSlip is a cache in front of the upstream's recommender, not a second
recommender. Two things add to the upstream's own answer:
- the supplement above, used only when the upstream's list is short;
- the interim (section 5.3), used only for a request the upstream has not
  answered by `MISS_WAIT_SECONDS`. It is never stored.

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
    and never prints it. From the running instance it also reports:
    - the `interim`, `interim-then-real` and `client-left` counters, with a
      WARN when interims are more than a fifth of misses, since that means
      the upstream is slow;
    - which users are resting under the backoff;
    - "busy follow-ups: stored N, later used M" and "empty retries: run N,
      filled M", printed as "none yet" while N is 0.
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
| `PRECACHE_ON_PLAY` | true (`false`; `auto` is read as `true`) |
| `BACKGROUND_MAX_CONCURRENT` | 2 |
| `MISS_WAIT_SECONDS` / `INTERIM_BUDGET_SECONDS` | 22 / 6 (0 turns the interim off) |
| `EMPTY_RETRY_SECONDS` | 10 (0 turns the retry off) |
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
request. Table `mix_users(username TEXT PRIMARY KEY, first_seen INT)` was
written for `PRECACHE_ON_PLAY=auto` up to v0.1. It is created if missing but
no longer read or written. No credential is ever stored. SubSlip only ever
opens its own DB file; it never touches the upstream's database or music.

## 11. Repository and distribution

- Go module `github.com/antidamage/subslip`, `cmd/subslip`, packages under
  `internal/`. Source files stay under 10 KB; every file opens with a comment
  naming what it does and which files it works with.
- Tests: unit tests per package plus an end-to-end test against a fake
  upstream (`httptest`) covering hit, miss, fallback, auth failure, Range
  pass-through and the debounce/slot rules (with injected clocks).
- Distribution: GitHub releases built by the owner's release tooling, with
  fixed asset names that the install scripts fetch:
  `subslip_<v>_windows_<amd64|arm64>.zip`,
  `subslip_<v>_linux_<amd64|arm64|armv7>.tar.gz`, `.deb`, `.rpm`, `.apk`
  and Arch packages, and `SHA256SUMS`. Archives hold the binary, LICENSE and
  README; packages install `/usr/bin/subslip` and no service files. There are
  no macOS builds: macOS installs with `go install`. No Android builds.
- Platforms: x86 is 64-bit only (`amd64`, labelled x64 in the notes). The
  one 32-bit build is Linux `armv7`, for Raspberry Pi boards on a 32-bit OS.
  Releases 0.1.0 to 0.2.1 also carry `386` builds for Windows and Linux;
  later releases do not. The install scripts take the 64-bit build under a
  32-bit PowerShell on 64-bit Windows, and refuse 32-bit x86 unless the
  version asked for is one of those that has it.
- `CHANGELOG.md` (Keep a Changelog layout) has a section per version, and a
  release's notes open with that section, followed by the downloads table.
  New entries go under `## [Unreleased]` with the change that makes them.
- `subslip init [--env FILE] [--upstream URL] [--listen ADDR] [--force]`
  writes a starting `.env` from the embedded `.env.example`. With no
  `--upstream` it tries `http://127.0.0.1:5274` (Octo), then `:4533`,
  `:4533/navidrome` and `:4533/music` (Navidrome), and takes the first that
  answers like a Subsonic server. It sets `UPSTREAM_URL`, `LISTEN_ADDR` and a
  random `STATUS_TOKEN`, never writes credentials, never replaces an existing
  file without `--force`, and writes nothing when the upstream does not answer.
- `install/install.sh` (Linux: release archive, checksum checked, systemd
  service, system-wide as root or per user otherwise; macOS: `go install` and
  a launchd agent) and `install/install.ps1` (Windows: release zip, checksum
  checked, a scheduled task at logon run through `conhost --headless` so no
  window opens, re-started every five minutes if it stopped). Both keep an
  existing `.env`, call `subslip init` otherwise, start the service and run
  `doctor`. Re-running either upgrades the binary.
- Multi-stage `Dockerfile` (static binary on Alpine with CA certificates,
  non-root user, `/data` volume, health check) and
  `docker-compose.example.yml` that builds from the repository.
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
is a hit. If the upstream takes longer than `MISS_WAIT_SECONDS`, the first tap
gets a short interim list (the seed artist and genre) instead of a timeout,
and the next tap gets the real mix. The README says this, and does not describe client behaviour as
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
