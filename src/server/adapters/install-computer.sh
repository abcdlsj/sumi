#!/bin/sh
# Installs a Sumi Computer as a user service and starts it.
#
#   curl -fsSL https://sumi.example.com/install.sh | sh -s -- --server https://sumi.example.com
#
# Running it again is safe: an installed binary is left to the daemon's own updater, and the
# service is only reloaded when its definition changes.
set -eu

LAUNCHD_LABEL=dev.sumi.computer
SYSTEMD_UNIT=sumi-computer.service
SUMI_DIR="$HOME/.sumi"
BIN="$SUMI_DIR/bin/sumi"
CONFIG="$SUMI_DIR/config.toml"
LOG_DIR="$SUMI_DIR/logs"
LOG="$LOG_DIR/computer.log"

say() { printf '%s\n' "$*"; }
fail() { printf 'sumi install: %s\n' "$*" >&2; exit 1; }

server=""
while [ $# -gt 0 ]; do
  case "$1" in
    --server) [ $# -ge 2 ] || fail "--server needs a URL"; server="$2"; shift 2 ;;
    --server=*) server="${1#--server=}"; shift ;;
    *) fail "unknown argument: $1" ;;
  esac
done

configured=""
if [ -f "$CONFIG" ]; then
  configured=$(sed -n 's/^[[:space:]]*server_url[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$CONFIG" | head -n 1)
fi
server="${server%/}"
configured="${configured%/}"
if [ -z "$server" ]; then
  [ -n "$configured" ] || fail "pass --server <Sumi Server URL>"
  server="$configured"
elif [ -n "$configured" ] && [ "$configured" != "$server" ]; then
  fail "$CONFIG already points at $configured; edit or remove it to switch Servers"
fi
case "$server" in
  https://*) ;;
  http://127.0.0.1|http://127.0.0.1:*|http://localhost|http://localhost:*|http://\[::1\]|http://\[::1\]:*) ;;
  *) fail "the Server must use HTTPS (plain HTTP is allowed only for loopback): $server" ;;
esac

os=$(uname -s)
arch=$(uname -m)
case "$os/$arch" in
  Darwin/arm64) target=aarch64-apple-darwin ;;
  Darwin/x86_64) target=x86_64-apple-darwin ;;
  Linux/x86_64) target=x86_64-unknown-linux-gnu ;;
  *) fail "unsupported platform: $os $arch" ;;
esac

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f 1
  else
    shasum -a 256 "$1" | cut -d ' ' -f 1
  fi
}

json_field() {
  sed -n "s/^[[:space:]]*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$2" | head -n 1
}

umask 077
mkdir -p "$SUMI_DIR/bin" "$LOG_DIR"
chmod 700 "$SUMI_DIR"

if [ ! -x "$BIN" ]; then
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT
  curl -fsSL "$server/api/v1/computer-updates/stable/$target/manifest.json" -o "$work/manifest.json" \
    || fail "$server does not publish a Computer release for $target"
  version=$(json_field version "$work/manifest.json")
  url=$(json_field url "$work/manifest.json")
  expected=$(json_field sha256 "$work/manifest.json")
  case "$url" in
    https://*) ;;
    *) fail "the release manifest has no HTTPS download URL" ;;
  esac
  [ -n "$expected" ] || fail "the release manifest has no sha256"
  say "Downloading Sumi Computer $version for $target"
  curl -fL --proto '=https' --proto-redir '=https' --progress-bar "$url" -o "$work/sumi"
  [ "$(sha256 "$work/sumi")" = "$expected" ] || fail "downloaded Computer does not match its sha256"
  chmod 700 "$work/sumi"
  mv "$work/sumi" "$BIN"
  say "Installed Sumi Computer $version at $BIN"
fi

if [ ! -f "$CONFIG" ]; then
  printf '[computer]\nserver_url = "%s"\n' "$server" > "$CONFIG"
  say "Wrote $CONFIG"
fi

paired=no
for secrets in "$SUMI_DIR"/computer/*/*/secrets.json; do
  [ -f "$secrets" ] && paired=yes && break
done
log_start=0
[ -f "$LOG" ] && log_start=$(wc -c < "$LOG" | tr -d ' ')

# Write $2 to $1 only when it changed; report whether it did.
replace_if_changed() {
  if [ -f "$1" ] && [ "$(cat "$1")" = "$2" ]; then
    return 1
  fi
  mkdir -p "$(dirname "$1")"
  printf '%s\n' "$2" > "$1"
}

xml_escape() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

case "$os" in
  Darwin)
    plist="$HOME/Library/LaunchAgents/$LAUNCHD_LABEL.plist"
    domain="gui/$(id -u)"
    definition=$(cat <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LAUNCHD_LABEL</string>
  <key>ProgramArguments</key>
  <array><string>$(xml_escape "$BIN")</string><string>computer</string></array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SUMI_COMPUTER_SERVICE</key><string>launchd</string>
    <key>PATH</key><string>$(xml_escape "$PATH")</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>$(xml_escape "$LOG")</string>
  <key>StandardErrorPath</key><string>$(xml_escape "$LOG")</string>
</dict>
</plist>
EOF
)
    if replace_if_changed "$plist" "$definition"; then
      launchctl bootout "$domain/$LAUNCHD_LABEL" 2>/dev/null || true
      launchctl bootstrap "$domain" "$plist"
      say "Loaded launchd agent $LAUNCHD_LABEL"
    elif ! launchctl print "$domain/$LAUNCHD_LABEL" >/dev/null 2>&1; then
      launchctl bootstrap "$domain" "$plist"
    fi
    launchctl kickstart "$domain/$LAUNCHD_LABEL"
    ;;
  Linux)
    command -v systemctl >/dev/null 2>&1 || fail "systemd is required to run the Computer as a service"
    unit="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/$SYSTEMD_UNIT"
    definition=$(cat <<EOF
[Unit]
Description=Sumi Computer
After=network-online.target

[Service]
ExecStart="$BIN" computer
Environment=SUMI_COMPUTER_SERVICE=systemd
Environment="PATH=$PATH"
Restart=on-failure
RestartSec=10
StandardOutput=append:$LOG
StandardError=append:$LOG

[Install]
WantedBy=default.target
EOF
)
    if replace_if_changed "$unit" "$definition"; then
      systemctl --user daemon-reload
      systemctl --user enable "$SYSTEMD_UNIT" >/dev/null
      systemctl --user restart "$SYSTEMD_UNIT"
      say "Installed systemd user unit $SYSTEMD_UNIT"
    else
      systemctl --user enable "$SYSTEMD_UNIT" >/dev/null
      systemctl --user start "$SYSTEMD_UNIT"
    fi
    if command -v loginctl >/dev/null 2>&1 \
      && [ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null)" != yes ]; then
      say "To keep the Computer running after you log out, run: loginctl enable-linger $(id -un)"
    fi
    ;;
esac

say "Sumi Computer is running; logs: $LOG"
[ "$paired" = no ] || exit 0

say "Waiting for the pairing link..."
for _ in $(seq 1 30); do
  if [ -f "$LOG" ]; then
    url=$(tail -c "+$((log_start + 1))" "$LOG" | sed -n 's/.*Confirm this Computer in Sumi.*url=\([^ ]*\).*/\1/p' | tail -n 1)
    if [ -n "$url" ]; then
      say "Confirm this Computer in Sumi: $url"
      exit 0
    fi
  fi
  sleep 1
done
url=$(sed -n 's/.*Confirm this Computer in Sumi.*url=\([^ ]*\).*/\1/p' "$LOG" 2>/dev/null | tail -n 1)
if [ -n "$url" ]; then
  say "Latest pairing link (it may have expired; the Computer starts a new one after it does): $url"
else
  say "No pairing link yet; it will appear in $LOG"
fi
