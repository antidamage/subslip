# Installing SubSlip: runbook for AI agents

Follow this when a user asks you to install, configure, check or remove
SubSlip. Steps are in order; each says what to run on Linux, macOS, Windows or
Docker. Do every step yourself. Ask the user only for what you cannot find:
their Subsonic login (for the checks) and which server to use when there is
more than one.

## Rules that apply throughout

- **Never change the user's existing server.** Do not edit Octo's or
  Navidrome's settings, databases or music folders, and do not move their
  ports. SubSlip is added in front; the old address keeps working and is the
  fallback.
- **Never write the user's password to a file.** Pass it to `subslip doctor`
  through `--password` or the `SUBSLIP_DOCTOR_PASSWORD` variable for that one
  command. SubSlip itself stores no credentials.
- **Do not repoint the user's clients for them.** Tell them the new address and
  that they should keep the old one saved. They switch when `doctor` passes.

## 1. Find the upstream server

SubSlip needs the URL the user's Subsonic client uses today, including any
base path. Look for Octo (usually port 5274) and Navidrome (usually 4533).

Linux / macOS:

```sh
ss -ltnp 2>/dev/null | grep -E ':(4533|4534|5274)\b' || lsof -iTCP -sTCP:LISTEN -nP | grep -E ':(4533|4534|5274)\b'
docker ps --format '{{.Names}}\t{{.Image}}\t{{.Ports}}' 2>/dev/null | grep -iE 'octo|navidrome'
grep -iE '^\s*(port|baseurl|address)' /var/lib/navidrome/navidrome.toml ~/.config/navidrome/navidrome.toml 2>/dev/null
```

Windows (PowerShell):

```powershell
Get-NetTCPConnection -State Listen | Where-Object LocalPort -in 4533,4534,5274 |
  ForEach-Object { "{0}:{1} {2}" -f $_.LocalAddress, $_.LocalPort, (Get-Process -Id $_.OwningProcess).ProcessName }
docker ps --format '{{.Names}} {{.Image}} {{.Ports}}' 2>$null | Select-String -Pattern 'octo|navidrome'
```

Then confirm it answers (an OpenSubsonic server answers this without a login):

```sh
curl -s "http://127.0.0.1:5274/rest/getOpenSubsonicExtensions?f=json&v=1.16.1&c=agent"
```

If Navidrome has a `BaseUrl` set (for example `/navidrome`), the upstream URL
includes it: `http://127.0.0.1:4533/navidrome`.

When both Octo and Navidrome are present, Octo is normally the one clients
use, because it fronts Navidrome. Use what the user's client points at; ask
if unsure.

## 2. Install

`subslip init` finds a local Octo or Navidrome by itself. Pass the upstream
explicitly when it is elsewhere or when step 1 found more than one.

**Linux or macOS** (as root for a system-wide service, or as the user for a
per-user one; macOS needs Go 1.26 or newer, since there are no macOS release
builds):

```sh
curl -fsSL https://raw.githubusercontent.com/antidamage/subslip/main/install/install.sh \
  | SUBSLIP_UPSTREAM=http://127.0.0.1:4533 sh
```

**Windows** (PowerShell, as the user who will run it):

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/antidamage/subslip/main/install/install.ps1))) -Upstream http://127.0.0.1:5274
```

Add `-OpenFirewall` from an elevated prompt if clients on other machines will
connect.

**Docker** (when the server runs in Docker): add the `subslip` service from
`docker-compose.example.yml` to the user's compose file, set `UPSTREAM_URL` to
the server as the container sees it (`http://octo:5274`,
`http://navidrome:4533`), then
`docker compose up -d subslip`. The example leaves `STATUS_TOKEN` empty;
generate one for this install if you want `/subslip/status`.

Where things end up:

| | Settings | Cache | Service |
| --- | --- | --- | --- |
| Linux, root | `/etc/subslip/.env` | `/var/lib/subslip` | `subslip.service` (systemd) |
| Linux, user | `~/.config/subslip/.env` | `~/.local/share/subslip` | `systemctl --user` unit |
| macOS | `~/.config/subslip/.env` | `~/.local/share/subslip` | launchd `io.github.antidamage.subslip` |
| Windows | `%LOCALAPPDATA%\SubSlip\.env` | `%LOCALAPPDATA%\SubSlip\data` | scheduled task `SubSlip` |
| Docker | environment | volume `/data` | `restart: unless-stopped` |

## 3. Check it

Ask the user for a Subsonic username and password, then:

```sh
SUBSLIP_DOCTOR_USER=<name> SUBSLIP_DOCTOR_PASSWORD=<password> subslip doctor --env <settings file>
```

Windows:

```powershell
$env:SUBSLIP_DOCTOR_USER='<name>'; $env:SUBSLIP_DOCTOR_PASSWORD='<password>'; & "$env:LOCALAPPDATA\SubSlip\subslip.exe" doctor --env "$env:LOCALAPPDATA\SubSlip\.env"
```

Docker:

```sh
docker exec -e SUBSLIP_DOCTOR_USER=<name> -e SUBSLIP_DOCTOR_PASSWORD=<password> subslip subslip doctor
```

Every line is PASS, WARN, FAIL, SKIP or INFO with a hint. Fix every FAIL
before going on. `--json` gives the same report for parsing. Expected WARNs
on a fresh install:

- `clients: no client has sent requests through SubSlip`: normal until the
  user repoints a client.
- `upstream Instant Mix: 0 tracks`: the server has no recommendation source
  for that random song. For Navidrome, suggest enabling agents (`Agents =
  "lastfm,deezer,listenbrainz"` in `navidrome.toml`, with their keys), and tell
  the user what that changes. Do not edit it unasked.

## 4. Hand over to the user

Tell the user, in this order:

1. The address to enter in their Subsonic client: the `client URL` line from
   `doctor`, with the same username and password as before.
2. Keep the old server address saved in the client. If SubSlip is ever down,
   switch back to it.
3. The first Instant Mix per user goes to the server as before. After that,
   SubSlip pre-builds the mix of each song they play, so an Instant Mix from
   the playing song comes back straight from the cache.

After they have used a client through SubSlip, run `doctor` again: the
`client seen` lines confirm their client is going through it, with the request
shape it uses.

## 5. Diagnose a problem

| Symptom | Check | Likely cause |
| --- | --- | --- |
| Client cannot connect | `curl http://<host>:5276/subslip/health` | Service not running, firewall, wrong port |
| Everything fails, not just Instant Mix | `SUBSLIP_MODE=passthrough`, restart, retry | If it still fails, the upstream URL is wrong |
| Instant Mix never fast | Response header `X-SubSlip` | `miss` every time: pre-cache not active for the user yet (see `doctor` pre-cache lines) or `PRECACHE_ON_PLAY=false` |
| Instant Mix empty | `doctor`, status `recentEvents` | Upstream returns nothing and the supplement found nothing; the event says why |
| `auth-failed` | the client's login | Wrong credentials at the upstream; SubSlip passes its error through |
| `fallback:<code>` | logs at `LOG_LEVEL=debug` | SubSlip gave up and proxied; the request still worked |

`/subslip/status` (with `Authorization: Bearer <STATUS_TOKEN>`) shows the mode,
upstream kind, cache counts, counters by outcome, queues, clients seen and the
last 100 events. Logs never contain credentials.

## 6. Upgrade or remove

Upgrade: run the same install command again; settings and cache are kept.

Remove:

- Linux: `systemctl disable --now subslip` (or `--user`), delete the unit file,
  `/usr/local/bin/subslip` (or `~/.local/bin/subslip`) and the settings and
  cache folders from the table above.
- macOS: `launchctl unload -w ~/Library/LaunchAgents/io.github.antidamage.subslip.plist`,
  then delete it, the binary and the folders.
- Windows: `Unregister-ScheduledTask -TaskName SubSlip -Confirm:$false`, then
  delete `%LOCALAPPDATA%\SubSlip`.
- Docker: remove the service and its volume.

Tell the user to point their clients back at the server's own address first.
