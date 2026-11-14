# shortlinker

Self-hosted, tailnet-only short links. Paste `https://<hub>.<tailnet>.ts.net/budget`
into Notion; clicking it opens app deep links (`obsidian://…`) on Mac and iOS,
and opens files/folders on whichever Mac you clicked. One Go binary, zero
dependencies, nothing leaves your Tailscale network.

Design: `docs/superpowers/specs/2026-07-06-shortlinker-design.md`

## Install

**Hub (the designated, usually-awake Mac):**

    make install
    # One-time: publish the hub at https://<hub>.<tailnet>.ts.net.
    # This replaces whatever this Mac already serves at / on port 443.
    tailscale serve --bg --https=443 http://127.0.0.1:7777

Recommended: System Settings → Battery/Energy → prevent sleep when plugged in.
Every link requires the hub to be awake.

**Every other Mac (opener agent):**

    HUB=https://<hub>.<tailnet>.ts.net make install

## Adding links

Edit `~/.config/shortlinker/links.txt` on the hub (takes effect immediately,
no restart):

    # slug = target      (:// → redirect, otherwise file/folder path)
    budget = ~/Documents/Finance/2026-budget.numbers
    daily  = obsidian://daily?vault=Main
    proj   = ~/Code/my-app

Or from any tailnet device / agent (this is the Claude Code interface):

    curl -X POST https://<hub>.<tailnet>.ts.net/api/links \
      -d '{"slug":"budget","target":"~/Documents/Finance/2026-budget.numbers"}'
    curl https://<hub>.<tailnet>.ts.net/api/links
    curl -X DELETE https://<hub>.<tailnet>.ts.net/api/links/budget

Slugs: lowercase letters, digits, hyphens. `o` and `api` are reserved.

## How a click works

- Deep link / URL target → 302 straight to the target (Mac + iOS).
- File/folder target → 302 to `http://localhost:7777/o/<slug>`, handled by
  the clicking Mac's own agent, which runs `open`. iPhones get a
  "Mac-only link" page instead. (Note: iPads using desktop-class Safari
  report a Mac user agent and will see a connection error instead.)

## Security model

- Binds `127.0.0.1` only; the sole network path in is `tailscale serve`
  (WireGuard-authenticated tailnet devices, never the public internet).
- `open` only ever runs on targets of registered slugs — nothing from the
  request is executed. A Host-header allowlist blocks DNS rebinding.

## Troubleshooting

- Logs: `~/Library/Logs/shortlinker.log`
- Restart: `launchctl unload ~/Library/LaunchAgents/com.$(id -un).shortlinker.plist && launchctl load ~/Library/LaunchAgents/com.$(id -un).shortlinker.plist`
- "hub unreachable — is it awake?": wake the hub Mac.
