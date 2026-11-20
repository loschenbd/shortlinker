#!/bin/sh
# Install shortlinker as a LaunchAgent.
#   Hub Mac:    ./install.sh
#   Other Macs: HUB=https://<hub>.<tailnet>.ts.net ./install.sh
#   Menu bar:   ./install.sh swiftbar   (optional SwiftBar plugin, after the above)
set -eu

BIN_DIR="$HOME/.local/bin"
LABEL="com.$(id -un).shortlinker"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
LOG="$HOME/Library/Logs/shortlinker.log"
DOMAIN="gui/$(id -u)"

if [ "${1:-}" = swiftbar ]; then
  dir=$(defaults read com.ameba.SwiftBar PluginDirectory 2>/dev/null || true)
  if [ -z "$dir" ]; then
    echo "SwiftBar has no plugin folder yet. Install SwiftBar (https://swiftbar.app), choose a plugin folder, then run this again." >&2
    exit 1
  fi
  if [ ! -x "$BIN_DIR/shortlinker" ]; then
    echo "Run ./install.sh first: the plugin calls $BIN_DIR/shortlinker." >&2
    exit 1
  fi
  plugin="$dir/shortlinker.1m.sh"
  cat > "$plugin" <<EOF
#!/bin/bash
# <xbar.title>shortlinker</xbar.title>
# <xbar.desc>Link what's selected in Finder, and find and copy your shortlinker links.</xbar.desc>
# <xbar.abouturl>https://github.com/loschenbd/shortlinker</xbar.abouturl>
# <swiftbar.hideRunInTerminal>true</swiftbar.hideRunInTerminal>
# <swiftbar.hideLastUpdated>true</swiftbar.hideLastUpdated>
# <swiftbar.hideDisablePlugin>true</swiftbar.hideDisablePlugin>
# Installed by shortlinker's install.sh; the menu comes from \`shortlinker swiftbar\`.
bin="$BIN_DIR/shortlinker"
[ -x "\$bin" ] || { printf '| sfimage=exclamationmark.triangle\n---\nshortlinker is not installed at %s\n' "\$bin"; exit 0; }
exec "\$bin" swiftbar
EOF
  chmod +x "$plugin"
  open -g "swiftbar://refreshallplugins" 2>/dev/null || true
  echo "Installed the menu bar plugin: $plugin"
  exit 0
fi

mkdir -p "$BIN_DIR" "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"
go build -o "$BIN_DIR/shortlinker" .
# `sl-link <path>` prints a link for a file or folder (same binary).
ln -sf shortlinker "$BIN_DIR/sl-link"

HUB_ARGS=""
if [ -n "${HUB:-}" ]; then
  HUB_ARGS="<string>--hub</string><string>${HUB}</string>"
fi

cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>${LABEL}</string>
  <key>ProgramArguments</key>
  <array><string>${BIN_DIR}/shortlinker</string>${HUB_ARGS}</array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>${LOG}</string>
  <key>StandardErrorPath</key><string>${LOG}</string>
</dict>
</plist>
EOF

# bootout returns before the old job is gone, and bootstrap fails until it
# is, so retry. kickstart then makes sure it runs: on recent macOS a job can
# be bootstrapped and still not started.
launchctl bootout "$DOMAIN/$LABEL" 2>/dev/null || true
i=0
until launchctl bootstrap "$DOMAIN" "$PLIST" 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 20 ]; then
    launchctl bootstrap "$DOMAIN" "$PLIST" # once more, showing the error
  fi
  sleep 0.25
done
launchctl kickstart -k "$DOMAIN/$LABEL"

i=0
until curl -s -o /dev/null http://127.0.0.1:7778/o/x; do
  i=$((i + 1))
  if [ "$i" -ge 40 ]; then
    echo "shortlinker didn't start; see $LOG" >&2
    exit 1
  fi
  sleep 0.25
done

if [ -n "${HUB:-}" ]; then
  echo "Installed as opener agent (hub: ${HUB}). Logs: ${LOG}"
else
  echo "Installed as hub. Logs: ${LOG}"
  echo "One-time tailnet exposure: tailscale serve --bg --https=443 http://127.0.0.1:7777"
  echo "(That replaces whatever this Mac already serves at / on port 443.)"
  echo "Never publish port 7778: it opens files on this Mac and holds the link API."
  if defaults read com.ameba.SwiftBar PluginDirectory >/dev/null 2>&1; then
    echo "SwiftBar is installed: 'make swiftbar' adds a shortlinker menu."
  fi
fi
