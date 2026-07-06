# shortlinker — design spec

**Date:** 2026-07-06
**Status:** Approved design, pre-implementation

## Purpose

A self-hosted, tailnet-only short-link service. Short links like
`https://<hub>.<tailnet>.ts.net/budget` are pasted into Notion (or anywhere)
and, when clicked:

- open app deep links (`obsidian://…`, `things:///…`) on Mac **and** iOS
- open local files and folders on whichever Mac the link was clicked on
- redirect to ordinary web URLs

Constraints that shaped the design: no always-on server (a designated Mac is
the hub and must be awake), everything stays on the user's machines/tailnet,
lowest possible maintenance, and Claude Code / other agents must be able to
create and manage links programmatically.

## Architecture

One Go binary (standard library only, no dependencies), run via a LaunchAgent
on every Mac, in one of two roles:

### Hub (the designated Mac)

- Owns the link store (`~/.config/shortlinker/links.txt`).
- Binds `127.0.0.1:7777` only. Network exposure is exclusively via
  `tailscale serve` (one-time setup), which publishes it as
  `https://<hub>.<tailnet>.ts.net` with a real TLS cert, reachable only by
  devices authenticated into the tailnet. Never internet-facing.
- Serves the canonical short URLs and the JSON API.

### Opener agent (every Mac, including the hub)

- Same binary, same port (`127.0.0.1:7777`), loopback only.
- Non-hub Macs start it with `--hub https://<hub>.<tailnet>.ts.net` so it can
  resolve slugs by asking the hub. The hub instance resolves locally.
- Its only job: turn a registered slug into a local `/usr/bin/open` call.

There is deliberately **no link-file syncing**. Agents resolve slugs against
the hub at click time, so there is exactly one source of truth and nothing to
keep consistent.

### Click flow

For `GET https://<hub>.ts.net/<slug>`:

1. Hub re-reads `links.txt` and looks up the slug. Unknown slug → 404.
2. Target contains `://` (deep link or web URL) → `302` to the target.
   Browsers follow top-level redirects into custom schemes on both macOS and
   iOS, so deep links work on every device.
3. Target is a file/folder path:
   - Desktop User-Agent → `302` to `http://localhost:7777/o/<slug>`. This
     lands on the *clicking* Mac's own opener agent. (Top-level `https → http
     localhost` redirects are permitted by browsers; this is not blocked as
     mixed content.)
   - iOS/mobile User-Agent → no redirect; serve a small page explaining the
     link opens a file on a Mac.
4. Opener agent (`GET /o/<slug>`): resolve the slug (locally on the hub;
   `GET <hub>/api/resolve/<slug>` elsewhere), run `/usr/bin/open <target>`
   with `~` expanded, and return a minimal "✓ Opened — you can close this
   tab" HTML page. (Browsers won't auto-close a tab they didn't script-open,
   so a small tab remains; accepted quirk.)

## Link store

`~/.config/shortlinker/links.txt` on the hub — one human-editable plain-text
file, re-read on every request (no reload, restart, or cache invalidation,
and edits made by hand or by agents take effect immediately):

```
# slug = target
# target with :// → 302 redirect; otherwise a file/folder path for `open`
budget   = ~/Documents/Finance/2026-budget.numbers
daily    = obsidian://daily?vault=Main
proj     = ~/Code/my-app
expenses = https://app.ramp.com/whatever
```

Rules:

- Slugs match `[a-z0-9-]+`. Anything else is rejected by the API and skipped
  (with a log line) by the parser.
- Blank lines and `#` comments are ignored. Malformed lines are skipped and
  logged, never fatal.
- Target type is inferred: substring `://` → redirect target; otherwise a
  path handed to `open` (folders open in Finder automatically).
- Reserved slug prefixes (cannot be registered): `o`, `api`.

## HTTP surface

| Route | Where | Behavior |
|---|---|---|
| `GET /{slug}` | hub | Resolve and redirect per click flow above |
| `GET /o/{slug}` | agent (all Macs) | Open target locally, return done page |
| `GET /api/resolve/{slug}` | hub | `200 {"slug":…,"target":…}` or 404 — used by remote agents |
| `GET /api/links` | hub | List all links as JSON |
| `POST /api/links` | hub | Add/update: `{"slug":"budget","target":"~/Documents/…"}` — updates the matching line in place, or appends; comment lines and ordering are preserved |
| `DELETE /api/links/{slug}` | hub | Remove a link |

The API is how Claude Code and other agents manage links from any machine
(one `curl` over the tailnet); on the hub itself, editing `links.txt`
directly is equally valid. The tailnet is the auth boundary: only
Tailscale-authenticated devices can reach the API, which is appropriate for a
personal tailnet. No additional token in v1.

## Security

- The binary binds loopback only, on every machine. The only network path in
  is `tailscale serve`, i.e. WireGuard-authenticated tailnet peers.
- `open` is only ever invoked on a target resolved from a registered slug.
  No path, URL, or argument from an incoming request is ever passed to
  `open`. Worst case for a malicious webpage poking `http://localhost:7777`
  is triggering one of the user's own predefined links.
- The opener agent validates the `Host` header against
  `localhost:7777` / `127.0.0.1:7777` and rejects anything else, blocking
  DNS-rebinding attacks.
- `open` is invoked via `exec.Command` with the target as a single argument —
  no shell, no injection surface.
- Link data lives in one plain-text file on the hub's disk and never leaves
  the tailnet.

## Error handling

| Condition | Behavior |
|---|---|
| Unknown slug | 404 page |
| Hub asleep / unreachable (canonical URL) | Browser connection error — accepted trade-off of Approach B |
| Hub unreachable during agent resolve | "Hub unreachable — is it awake?" error page |
| `open` exits non-zero | 500 page including the error text |
| Malformed `links.txt` line | Skipped, logged to stderr (visible in LaunchAgent log) |
| Invalid slug or target in API request | 400 with a JSON error message |

## Testing

- Table-driven unit tests for the `links.txt` parser (valid lines, comments,
  malformed lines, slug validation, `://` inference).
- Handler tests with `net/http/httptest` for: redirect targets, file targets
  (desktop vs. iOS User-Agent branching), `/o/` open dispatch (with the
  `open` exec stubbed), Host-header rejection, and the full API CRUD cycle.
- Manual end-to-end pass per device: Mac hub, second Mac, iPhone.

## Install & operations

- `make install` on each Mac: builds the binary, writes
  `~/Library/LaunchAgents/com.<username>.shortlinker.plist`
  (KeepAlive, RunAtLoad, stderr to a log file), and loads it via `launchctl`.
- Hub one-time extras: `tailscale serve --bg 7777`, and System Settings →
  prevent sleep when plugged in (the hub must be awake for any link to
  resolve).
- Config via flags: `--hub <url>` (presence distinguishes agent-only from hub
  role), `--port` (default 7777), `--links <path>` (default
  `~/.config/shortlinker/links.txt`).
- Nothing to update routinely: no dependencies, no database, no UI. Rebuild
  only when changing the code.

## Out of scope (v1)

- `sl` CLI wrapper (curl + file editing suffice; add later if wanted)
- Link file syncing between machines (superseded by ask-the-hub design)
- Any cloud component, public exposure, analytics, or click tracking
- Auto-closing the "✓ Opened" tab
- Windows/Linux openers
