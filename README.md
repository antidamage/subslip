![SubSlip logo](./subslip.svg)

# SubSlip

A fast Subsonic reverse proxy that transparently pre-caches smart playlists before they're requested.

SubSlip sits between your Subsonic client and your server: Octo, Navidrome, or
any other Subsonic server. It passes every request through unchanged. While a
song plays, it asks the server for that song's Instant Mix and stores the
answer. When you then tap Instant Mix, the list comes from the cache instead
of waiting on the server's recommendation engine.

**If SubSlip is down, clients pointed at it get nothing until you switch them
back to your server's own address.** Keep that address saved in your client.
Installing SubSlip never changes it.

Each song a user plays costs one Instant Mix call to your server in the
background, whether or not they use Instant Mix. `PRECACHE_ON_PLAY=false`
turns that off.

## How it works

```
Subsonic client ──► SubSlip :5276 ──► Octo or Navidrome ──► (Octo: slskd, radio sources)
                       │
                       └─ cache: one Instant Mix per user and song (SQLite)
```

- **Everything is proxied.** Streams, Range requests, cover art, scrobbles,
  the web UI: SubSlip forwards them with the server's own status, headers and
  body, without buffering.
- **While a song plays**, SubSlip waits ten seconds (so skipping through tracks
  costs nothing), then fetches that song's Instant Mix in the background and
  stores it.
- **When you ask for an Instant Mix** (`getSimilarSongs` or `getSimilarSongs2`),
  a stored mix comes back from the cache. Without one, SubSlip makes the same
  call your client would have made, returns it as soon as it arrives, and
  stores it for next time.
- **A slow server does not make the client give up.** If your server has not
  answered after 22 seconds (`MISS_WAIT_SECONDS`), SubSlip answers with a short
  interim list of songs by the same artist and in the same genre. It keeps
  waiting on your server, stores the real mix when it arrives, and serves that
  mix the next time you ask. If the client gives up anyway, the real mix is
  still stored.
- **Short mixes are topped up.** When the server returns fewer than 15 tracks,
  SubSlip asks ListenBrainz for recordings by similar artists and keeps the
  ones your server can play, found with its own search. The server's tracks
  stay first and in its order.
- **It fails open.** If anything inside SubSlip goes wrong (a crash, a broken
  cache file, a request it does not understand), the request goes to the
  server as if SubSlip were not there.

### What is fast

- An Instant Mix started from the song that is playing: it was built while the
  song played.
- Any Instant Mix requested before, for up to 24 hours (refreshed in the
  background after 6).

An Instant Mix on a song you have not played is as fast as your server, or
gets the interim list if your server is very slow. The next one on that song
comes from the cache. Every user is pre-cached from their first play. At most
two background builds run at once (`BACKGROUND_MAX_CONCURRENT`). After three
failed builds in a row, a user's pre-caching rests for ten minutes, so a
failing server gets no extra load.

If your server already answers Instant Mix quickly, SubSlip gains you little.
`SUBSLIP_MODE=passthrough` turns it into a plain proxy.

### Numbers

Measured on one Octo install and one Navidrome install, at the network level
with a script, not in a phone client:

| | Cold (server) | From SubSlip's cache |
| --- | --- | --- |
| Octo | 1.7–3.5 s | about 4 ms |
| Navidrome | 0.13–2.1 s | about 4 ms |

## Install

### With an AI agent

Ask your agent to install SubSlip from this repository. It will follow
[`AGENT_SETUP.md`](AGENT_SETUP.md): find your server, install SubSlip as a
service, run the checks and tell you the address to give your client.

### With the install script

Linux (systemd) or macOS (launchd; needs Go 1.26 or newer):

```sh
curl -fsSL https://raw.githubusercontent.com/antidamage/subslip/main/install/install.sh | sh
```

Windows (PowerShell):

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/antidamage/subslip/main/install/install.ps1)))
```

Both find a local Octo (port 5274) or Navidrome (port 4533) by themselves.
Pass the server's URL when it lives elsewhere: `SUBSLIP_UPSTREAM=http://host:4533`
for the shell script, `-Upstream http://host:4533` on Windows. Re-running
either one upgrades the binary and keeps your settings.

Release packages for Linux (`.deb`, `.rpm`, `.apk`, Arch) and Windows zips are
on the [releases page](https://github.com/antidamage/subslip/releases), for x64
and ARM64, plus ARM 32-bit on Linux. The packages install only the binary; run
`subslip init` to write settings. What changed in each version is in
[CHANGELOG.md](CHANGELOG.md).

### With Docker

Add the service from [`docker-compose.example.yml`](docker-compose.example.yml)
to your compose file, set `UPSTREAM_URL` to your server as the container sees
it (for example `http://navidrome:4533`), and run
`docker compose up -d subslip`.

### From source

```sh
git clone https://github.com/antidamage/subslip.git && cd subslip
CGO_ENABLED=0 go build -o subslip ./cmd/subslip
./subslip init --upstream http://127.0.0.1:4533   # writes .env
./subslip
```

### Then

1. Run `subslip doctor --user <name> --password <password>`. Every line ends in
   PASS, WARN or FAIL with a hint.
2. In your Subsonic client, add the address `doctor` prints, with your usual
   login. Keep the old address saved too.

## Configuration

Settings come from a `.env` file (`--env FILE`, else `./.env`, else next to the
binary) and from environment variables, which win. Every setting is described
in [`.env.example`](.env.example). The ones you are most likely to change:

| Setting | Default | |
| --- | --- | --- |
| `UPSTREAM_URL` | `http://127.0.0.1:4533` | Your server, with any base path |
| `LISTEN_ADDR` | `0.0.0.0:5276` | Where clients connect |
| `PRECACHE_ON_PLAY` | `true` | `false` stops background pre-caching (`auto`, from v0.1, is read as `true`) |
| `MISS_WAIT_SECONDS` / `INTERIM_BUDGET_SECONDS` | 22 / 6 | When to send the interim list, and how long building it may take; `0` turns it off |
| `EMPTY_RETRY_SECONDS` | 10 | Ask once more after your server answers a mix with no tracks; `0` turns it off |
| `MIN_MIX_TRACKS` / `MAX_MIX_TRACKS` | 15 / 25 | Top-up threshold and mix size |
| `CACHE_TTL_HOURS` / `CACHE_REFRESH_HOURS` | 24 / 6 | Longest a mix is served / when it is rebuilt |
| `REQUIRE_AUTH` | `true` | Check credentials with the server before serving from cache |
| `LISTENBRAINZ_ENABLED` | `true` | The top-up for short mixes |
| `STATUS_TOKEN` | empty | Enables `/subslip/status` |
| `SUBSLIP_MODE` | `normal` | `passthrough` makes SubSlip a plain proxy |

## Security

A cache hit never reaches your server, so with `REQUIRE_AUTH=true` SubSlip
checks every Instant Mix caller's credentials with the server (`ping`) and
remembers a success for 60 seconds. Keep it on if SubSlip can be reached from
the internet, and put it behind HTTPS there, as you would your server. Set it
to `false` only where every client is trusted, such as a LAN or VPN.

Mixes are stored per user, so one user never receives another's. SubSlip never
writes credentials to disk or to its logs, and redacts them from every log
line, status event and error message. `/subslip/health` says only `ok`;
`/subslip/status` needs the status token.

## Diagnostics

- **`X-SubSlip` response header** on every response: `passthrough`,
  `hit;age=<seconds>`, `miss`, `interim` (the server was slow; a short related
  list was sent and the real mix is stored for next time), `busy` (proxied unchanged, and stored for next time when it is a full mix, because another Instant Mix for
  that user was in flight), `auth-failed`, `upstream-error`, or
  `fallback:<code>` when SubSlip gave up and proxied the request.
- **`subslip doctor`** checks settings, the cache, the server, a login, cold
  and cached Instant Mix times, ListenBrainz, and which clients the running
  service has seen. `--json` for scripts and agents.
- **`/subslip/status`**, with `Authorization: Bearer <STATUS_TOKEN>`: mode,
  server type, cache counts, counters by outcome, queues, pre-cache users,
  clients seen with the first request shape each sent, and the last 100
  events.
- **Logs** at `LOG_LEVEL=debug` record every decision: debounced, skipped,
  built, stored, and why a mix was empty.
- **`subslip cache stats`** and **`subslip cache purge [--expired]`**.

To report a problem, include the `doctor --json` output and, if you can, the
`clientsSeen` entry for your client from the status page. Neither contains
passwords.

## Contributing

Contributions are welcome. Read [`AGENTS.md`](AGENTS.md) for the rules the code
follows (fail-open, no request waits on SubSlip, no credentials in output,
pure Go, small files with a file map at the top of each) and
[`specs/subslip.md`](specs/subslip.md) for the behaviour. Run
`go vet ./...` and `go test ./...` before sending a change; server behaviour is
tested against a fake Subsonic server in `internal/server`.

## License

GNU General Public License v3.0. See [`LICENSE`](LICENSE).
