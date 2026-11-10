#!/bin/sh
# Install shortlinker as a LaunchAgent.
#   Hub Mac:    ./install.sh
#   Other Macs: HUB=https://<hub>.<tailnet>.ts.net ./install.sh
set -eu

BIN_DIR="$HOME/.local/bin"
PLIST="$HOME/Library/LaunchAgents/com.$(id -un).shortlinker.plist"
LOG="$HOME/Library/Logs/shortlinker.log"

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
  <key>Label</key><string>com.$(id -un).shortlinker</string>
  <key>ProgramArguments</key>
  <array><string>${BIN_DIR}/shortlinker</string>${HUB_ARGS}</array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>${LOG}</string>
  <key>StandardErrorPath</key><string>${LOG}</string>
</dict>
</plist>
EOF

launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"

if [ -n "${HUB:-}" ]; then
  echo "Installed as opener agent (hub: ${HUB}). Logs: ${LOG}"
else
  echo "Installed as hub. Logs: ${LOG}"
  echo "One-time tailnet exposure: tailscale serve --bg --https=443 http://127.0.0.1:7777"
  echo "(That replaces whatever this Mac already serves at / on port 443.)"
  echo "Never publish port 7778: it opens files on this Mac and changes links."
fi
