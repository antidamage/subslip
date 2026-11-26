![SubSlip logo](./subslip.svg)

# SubSlip

A fast Subsonic reverse proxy that transparently pre-caches smart playlists before they're requested.

SubSlip sits between your Subsonic client and your server (Octo, Navidrome or
another Subsonic server). It passes every request through unchanged. While a
song plays, it asks the server for that song's Instant Mix and keeps the
answer, so when you tap Instant Mix the list comes back from the cache instead
of waiting on the server's recommendation engine. When the server's list is
short, SubSlip tops it up from ListenBrainz with tracks your server can play.

**If SubSlip is down, clients pointed at it get nothing until you switch them
back to your server's own address.** Keep that address saved in your client.

This is an early version. Installation instructions, Docker files and release
binaries are coming in the next change; until then, build from source:

```sh
CGO_ENABLED=0 go build -o subslip ./cmd/subslip
cp .env.example .env    # set UPSTREAM_URL and STATUS_TOKEN
./subslip doctor --user <name> --password <password>
./subslip
```

Behaviour is specified in [`specs/subslip.md`](specs/subslip.md). Licensed
under the GNU GPL v3.
