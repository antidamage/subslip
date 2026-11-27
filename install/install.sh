#!/bin/sh
# Install SubSlip and run it as a service.
#
#   Linux: downloads the release archive for this CPU from GitHub, checks it
#          against SHA256SUMS, and installs a systemd service (system-wide as
#          root, per-user otherwise).
#   macOS: there are no macOS release builds, so it builds with `go install`
#          (Go 1.26 or newer needed) and installs a launchd agent.
#
# Settings, all optional, as environment variables:
#   SUBSLIP_VERSION     release to install, e.g. 0.1.0 (default: latest)
#   SUBSLIP_UPSTREAM    your Subsonic server's URL (default: found automatically)
#   SUBSLIP_LISTEN      address to listen on (default: 0.0.0.0:5276)
#   SUBSLIP_NO_SERVICE  set to 1 to install the binary and settings only
#
# An existing settings file is never overwritten. Re-running upgrades the binary.
# Usage: curl -fsSL https://raw.githubusercontent.com/antidamage/subslip/main/install/install.sh | sh
set -eu

REPO="antidamage/subslip"
VERSION="${SUBSLIP_VERSION:-latest}"
LISTEN="${SUBSLIP_LISTEN:-0.0.0.0:5276}"

say() { printf 'subslip-install: %s\n' "$*"; }
die() { say "error: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

OS="$(uname -s)"
if [ "$(id -u)" = 0 ] && [ "$OS" = Linux ]; then
  BIN_DIR=/usr/local/bin; ETC_DIR=/etc/subslip; DATA_DIR=/var/lib/subslip; MODE=system
else
  BIN_DIR="$HOME/.local/bin"; ETC_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/subslip"
  DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/subslip"; MODE=user
fi
ENV_FILE="$ETC_DIR/.env"
mkdir -p "$BIN_DIR" "$ETC_DIR" "$DATA_DIR"

install_linux_binary() {
  need curl; need tar
  case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    armv7l|armv7*) ARCH=armv7 ;;
    i386|i486|i586|i686) ARCH=386 ;;  # only 0.1.0-0.2.1 have it; checked below
    *) die "no release build for $(uname -m); build from source instead (see README)" ;;
  esac
  if [ "$VERSION" = latest ]; then
    VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1)"
    [ -n "$VERSION" ] || die "could not find the latest release"
  fi
  VERSION="${VERSION#v}"
  if [ "$ARCH" = 386 ]; then
    case "$VERSION" in
      0.1.0|0.2.0|0.2.1) ;;
      *) die "32-bit x86 Linux is not supported after 0.2.1; set SUBSLIP_VERSION=0.2.1 for the last 32-bit build" ;;
    esac
  fi
  ASSET="subslip_${VERSION}_linux_${ARCH}.tar.gz"
  BASE="https://github.com/$REPO/releases/download/v$VERSION"
  TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
  say "downloading $ASSET"
  curl -fsSL -o "$TMP/$ASSET" "$BASE/$ASSET" || die "download failed: $BASE/$ASSET"
  curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS" || die "download failed: SHA256SUMS"
  # An asset missing from SHA256SUMS is an error, not an unverified install.
  SUM_LINE="$(grep -E "[[:space:]]\*?$ASSET\$" "$TMP/SHA256SUMS" | head -n 1)" || true
  [ -n "$SUM_LINE" ] || die "$ASSET is not listed in SHA256SUMS"
  WANT="$(printf '%s' "$SUM_LINE" | cut -d ' ' -f 1)"
  GOT="$(sha256sum "$TMP/$ASSET" | cut -d ' ' -f 1)"
  [ -n "$WANT" ] && [ "$WANT" = "$GOT" ] || die "checksum mismatch for $ASSET"
  tar -xzf "$TMP/$ASSET" -C "$TMP"
  install -m 0755 "$TMP/subslip" "$BIN_DIR/subslip"
}

install_macos_binary() {
  command -v go >/dev/null 2>&1 || die "macOS needs Go 1.26+ to build SubSlip (brew install go), then re-run"
  REF="latest"; [ "$VERSION" != latest ] && REF="v${VERSION#v}"
  say "building github.com/$REPO/cmd/subslip@$REF"
  CGO_ENABLED=0 GOBIN="$BIN_DIR" go install "github.com/$REPO/cmd/subslip@$REF"
}

write_settings() {
  if [ -f "$ENV_FILE" ]; then
    say "keeping existing settings in $ENV_FILE"
    return
  fi
  if [ -n "${SUBSLIP_UPSTREAM:-}" ]; then
    "$BIN_DIR/subslip" init --env "$ENV_FILE" --listen "$LISTEN" --upstream "$SUBSLIP_UPSTREAM"
  else
    "$BIN_DIR/subslip" init --env "$ENV_FILE" --listen "$LISTEN"
  fi
  # Keep the cache in the data folder rather than next to the settings.
  sed -i.bak "s|^DB_PATH=.*|DB_PATH=$DATA_DIR/instant_mix_cache.db|" "$ENV_FILE" && rm -f "$ENV_FILE.bak"
}

install_systemd() {
  command -v systemctl >/dev/null 2>&1 || { say "no systemd found; start it yourself: $BIN_DIR/subslip serve --env $ENV_FILE"; return; }
  if [ "$MODE" = system ]; then
    id subslip >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin subslip
    chown -R subslip "$DATA_DIR"; chown subslip "$ENV_FILE"
    UNIT=/etc/systemd/system/subslip.service; USER_LINE="User=subslip"; CTL="systemctl"; WANTED=multi-user.target
  else
    UNIT="$HOME/.config/systemd/user/subslip.service"; USER_LINE=""; CTL="systemctl --user"; WANTED=default.target
    mkdir -p "$(dirname "$UNIT")"
  fi
  cat >"$UNIT" <<EOF
[Unit]
Description=SubSlip Subsonic proxy
After=network-online.target
Wants=network-online.target

[Service]
$USER_LINE
ExecStart=$BIN_DIR/subslip serve --env $ENV_FILE
Restart=always
RestartSec=5

[Install]
WantedBy=$WANTED
EOF
  $CTL daemon-reload
  $CTL enable --now subslip.service
  $CTL restart subslip.service
  if [ "$MODE" = user ]; then
    say "per-user service: run 'loginctl enable-linger ${USER:-$(id -un)}' to keep it running after logout"
  fi
}

install_launchd() {
  PLIST="$HOME/Library/LaunchAgents/io.github.antidamage.subslip.plist"
  mkdir -p "$(dirname "$PLIST")" "$DATA_DIR/logs"
  cat >"$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.github.antidamage.subslip</string>
  <key>ProgramArguments</key><array>
    <string>$BIN_DIR/subslip</string><string>serve</string><string>--env</string><string>$ENV_FILE</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>$DATA_DIR/logs/subslip.log</string>
</dict></plist>
EOF
  launchctl unload "$PLIST" 2>/dev/null || true
  launchctl load -w "$PLIST"
}

case "$OS" in
  Linux) install_linux_binary ;;
  Darwin) install_macos_binary ;;
  *) die "unsupported system $OS; on Windows use install/install.ps1" ;;
esac
say "installed $("$BIN_DIR/subslip" version) to $BIN_DIR/subslip"
write_settings

if [ "${SUBSLIP_NO_SERVICE:-0}" != 1 ]; then
  if [ "$OS" = Darwin ]; then install_launchd; else install_systemd; fi
  sleep 2
fi

say "checking the installation (give a login with: subslip doctor --env $ENV_FILE --user NAME --password PASS)"
"$BIN_DIR/subslip" doctor --env "$ENV_FILE" || true
