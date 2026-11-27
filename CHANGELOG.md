# Changelog

Each release's notes on GitHub open with its section from this file.
Versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.2] - 2026-11-27

No change to how SubSlip works; this release changes what is built.

### Changed

- Release builds are 64-bit only on x86: Windows x64 and ARM64, and Linux
  x64, ARM64 and ARM 32-bit (armv7, for Raspberry Pi on a 32-bit OS).
  32-bit x86 (386) builds are no longer made; 0.1.0 to 0.2.1 still have them.
- The install scripts refuse 32-bit x86 with a clear message. On Windows, a
  32-bit PowerShell on 64-bit Windows now installs the 64-bit build.

## [0.2.1] - 2026-11-27

### Added

- Busy follow-ups are stored. When another Instant Mix for the same user is
  already in flight, SubSlip still passes the request through unchanged, and
  now keeps the answer when it is a full JSON mix. A later request for that
  song is then served from the cache. A stored copy never overwrites a
  fresh mix.
- `EMPTY_RETRY_SECONDS` (default 10, `0` turns it off): when your server
  answers a mix with no tracks, SubSlip asks once more that many seconds
  later, at most once per song per hour. The retry only helps a later
  request.
- `subslip doctor` reports how many busy follow-ups were stored and later
  used, and how many empty retries ran and filled a mix.

### Fixed

- A request that arrives after your server has answered, but before the mix
  is stored, now gets that answer instead of a second upstream call.
- A background build whose own deadline ran out could leave its result
  unstored.
- `subslip doctor` labelled the cache's user count "pre-cache users". It now
  says "users with cached mixes".

## [0.2.0] - 2026-11-26

### Changed

- Every user is pre-cached from their first play. `PRECACHE_ON_PLAY` now
  defaults to `true`; `false` turns it off, and 0.1's `auto` is read as
  `true`. Each song played costs one background Instant Mix call to your
  server.
- A client that gives up no longer cancels the call to your server. The
  answer is still stored, so the next request for that song is a cache hit.
  `BACKGROUND_BUILD_TIMEOUT_SECONDS` (default 90) is the deadline for every
  Instant Mix call.
- A background build and a client request for the same song share one call
  to your server.

### Added

- `MISS_WAIT_SECONDS` (default 22) and `INTERIM_BUDGET_SECONDS` (default 6):
  when your server has not answered in time, SubSlip sends a short interim
  list of songs by the same artist and in the same genre, so the client does
  not time out. The interim list is never cached; the real mix is stored
  when it arrives. `0` turns it off.
- `BACKGROUND_MAX_CONCURRENT` (default 2) caps background builds across all
  users.
- After three failed builds in a row, a user's pre-caching rests for ten
  minutes.
- `subslip doctor` and `/subslip/status` report interim answers, clients that
  left before the answer, and users resting under the backoff.

## [0.1.0] - 2026-11-26

First release.

- A Subsonic reverse proxy for Navidrome, Octo or any Subsonic server. Every
  request passes through unchanged except Instant Mix (`getSimilarSongs`,
  `getSimilarSongs2`), which is cached per user and song.
- Pre-caches the mix of the song a user starts playing
  (`PRECACHE_ON_PLAY`, default `auto`: users who have asked for an Instant
  Mix through SubSlip at least once).
- Tops up short mixes with ListenBrainz suggestions your server can play.
- If anything goes wrong, the request falls back to plain pass-through.
- Diagnostics: the `X-SubSlip` response header, `subslip doctor`,
  `/subslip/status` (token protected), redacted logs, `subslip cache stats`
  and `subslip cache purge`, and `SUBSLIP_MODE=passthrough`.
- `subslip init` writes a starting `.env` and finds a local server by itself.
- Install scripts for Linux (systemd), macOS (`go install` and launchd) and
  Windows (a scheduled task with no window), plus a Dockerfile and a compose
  example.
- Release builds for Windows (386, amd64, arm64) and Linux (386, amd64,
  arm64, armv7) as zip, tar.gz, `.deb`, `.rpm`, `.apk` and Arch packages,
  with `SHA256SUMS`.

[Unreleased]: https://github.com/antidamage/subslip/compare/v0.2.2...HEAD
[0.2.2]: https://github.com/antidamage/subslip/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/antidamage/subslip/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/antidamage/subslip/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/antidamage/subslip/releases/tag/v0.1.0
