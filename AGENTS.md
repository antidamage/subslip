# Notes for AI agents

This file is for coding agents working in this repository (Claude Code, Codex,
Cursor, Cline and others). People should start with `README.md`.

## What this is

SubSlip is a Go reverse proxy for Subsonic servers (Octo, Navidrome, or any
other). It proxies every request unchanged and caches Instant Mix
(`getSimilarSongs`, `getSimilarSongs2`) results, pre-building the mix of the
song a user is playing. The behaviour is specified in `specs/subslip.md`. When
code and spec disagree, the spec is the intent: fix the code, or change the
spec in the same commit and say why.

## If you were asked to install SubSlip

Follow `AGENT_SETUP.md` when it exists in your copy. Until then the README's
install section applies. Never change the address of the user's existing
Subsonic server, and never touch its database or music folders. SubSlip only
writes its own cache file at `DB_PATH`.

## Rules for changes

- **SubSlip is never the broken link.** Any failure inside SubSlip must end in
  the request being proxied to the upstream, not in an error the upstream would
  not have produced. Spec section 2 lists the cases. New code follows it.
- **No request waits on SubSlip.** A cache miss makes the client's own call
  once, detached from the client's connection, and replies with it. If the
  upstream is slower than `MISS_WAIT_SECONDS`, a short related interim is
  sent instead, and it is never cached. Extra work happens in the background.
- **Credentials are never logged, stored or shown.** Use `internal/redact` for
  anything that might contain a URL. `internal/server/redaction_test.go`
  pushes a known password through every path; keep it passing.
- **Pure Go.** `CGO_ENABLED=0` must build for linux, darwin and windows on
  amd64 and arm64. The SQLite driver is `modernc.org/sqlite` for that reason.
- **Small files.** Keep each source file under about 10 KB. Every file opens
  with a comment saying what it does and which files it works with; update
  that map when you move code.
- **Tests with the change.** `go vet ./...` and `go test ./...` pass before a
  commit. Server behaviour is tested against the fake upstream in
  `internal/server/fake_upstream_test.go`; time-based scheduling uses
  `schedule.FakeClock`, never sleeps.

## Layout

| Path | Holds |
| --- | --- |
| `cmd/subslip` | CLI: `serve`, `doctor`, `cache`, `version` |
| `internal/server` | routing, proxy, Instant Mix handler, play hook, status |
| `internal/mix` | stage 1, ListenBrainz supplement, match rules |
| `internal/schedule` | per-user debounce and background slots |
| `internal/cache` | the SQLite store |
| `internal/auth` | upstream credential check with a short proof cache |
| `internal/upstream` | SubSlip's own JSON calls to the upstream |
| `internal/listenbrainz` | LB Radio and recording metadata |
| `internal/subsonic` | request parsing and XML/JSON rendering |
| `internal/doctor` | the `doctor` checks |
| `internal/config`, `logging`, `redact`, `diag` | settings, logs, redaction, status record |
