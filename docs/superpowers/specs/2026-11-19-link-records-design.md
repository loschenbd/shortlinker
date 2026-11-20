# Link records: what shortlinker should remember

**Date:** 2026-11-19
**Status:** Built on 2026-11-19. Two additions while building: clicks also
record the routes `renamed` (sent on to the new slug) and `removed` (a
tombstone answered), and the API gives each link an `archives_at` time.

**Since then (same day):** `links.txt` merged into the records. Every link
is now a record in `links.jsonl`, and "yours" became a `kept` flag (kept
links never archive and prune skips them), so a link you keep holds on to
its creation date, session, note and clicks. Records also gained
`last_device`. `shortlinker export` and `import` cover the plain-text uses
`links.txt` had. Below, "sl-link's links" and `auto-links.jsonl` describe
the design as first proposed.

## Why

sl-link makes a link for every file an agent shows you. On one heavy user's
hub that came to about 60 links a day: 550 in the first 9 days, which would be
around 20,000 a year. Half pointed into agent worktrees, which get deleted
once the work is merged.

Today a link records three things: slug, target and phone page. That leaves
three problems:

- **Nobody can tell a throwaway link from a keeper.** Nothing records when a
  link was made or whether anyone clicked it. `prune` only finds links whose
  file is gone. It can't find "the file still exists, but nobody will ever
  open this link."
- **Pruning can send an old link to the wrong file.** A pruned slug is free
  again, so an old link pasted somewhere can later open a different file
  with the same name.
- **A link has no context.** The manage page can't say which project or
  session a link came from, or why it was made.

This proposal covers what to record, and what not to. It assumes sl-link's
links move from `auto-links.txt` to `auto-links.jsonl`, which the hub keeps in
memory and saves atomically. Your `links.txt` stays plain text.

## What other link tools record

- **[golink](https://github.com/tailscale/golink)** (Tailscale's tailnet go-links,
  also written in Go) stores, per link: the short name, the target, when it was
  created, when it was last edited, and its owner. It keeps click counts in a
  separate table, with a timestamp on each count.
- **[Dub](https://dub.co/docs/webhooks/events/link-clicked.md)** keeps
  `clicks`, `lastClicked`, `archived` and `expiresAt` on each link. It sends
  individual click events to a separate analytics store.
- **[Shlink](https://shlink.io/documentation/tracking-visits)** logs every visit,
  including visits to short codes that don't exist ("orphan visits"). It can
  run with IP tracking turned off.

Every one of them separates the link (small, current state) from its click
history (an append-only log). shortlinker should do the same.

## What we can learn cheaply

- **Which device clicked.** `tailscale serve` sets `X-Forwarded-For` to the
  client's tailnet address (`addProxyForwardedHeaders` in tailscale's
  `ipn/ipnlocal/serve.go`). Tailnet addresses are stable per device, so the
  hub can name the device ("iPhone", "work laptop") from `tailscale status`.
  It also sets `Tailscale-User-Login`, but on a one-person tailnet every click
  has the same login.
- **Where the click came from.** Browsers send the referring page's origin
  (`https://www.notion.so`) on most cross-site clicks. Native apps send nothing.
- **Which agent session made the link.** Claude Code puts
  `CLAUDE_CODE_SESSION_ID` in the environment of every command it runs, so
  sl-link sees it. The ID leads straight to the conversation, which explains
  why the link exists and can be resumed.
- **Which project.** sl-link knows its working directory and can find the
  enclosing git repository. About 90% of the links on the hub above were inside
  one.

None of these headers are proof of anything: local processes can reach the
public port directly and set them. That's fine for statistics, and the hub
never uses them to decide access.

## Proposal

### Per link (`auto-links.jsonl`, one object per line)

| Field | Example | Why |
|---|---|---|
| `slug`, `target`, `phone` | as today | |
| `created` | `2026-11-19T19:09:24Z` | Retention, "recent" lists |
| `origin` | `sl-link`, `finder`, `api`, `manage` | Tells agent links from ones you made |
| `project` | `~/Code/my-app` | Group and search by project. The main checkout, never the worktree path |
| `session` | `0dae59d5-…` | Find the conversation that made it |
| `note` | `"Q3 plan for review"` | Optional, from `sl-link --note` |
| `linked` | `3` | How many times sl-link printed this link |
| `last_linked` | timestamp | Linking a file again resets the retention clock |
| `clicks` | `2` | Kept or throwaway, at a glance |
| `last_clicked` | timestamp | Retention |
| `archived` | timestamp or absent | Hidden from lists, still works (below) |
| `removed` | `{"at": …, "why": "pruned"}` or absent | A tombstone: the slug is never given out again |

Example:

```json
{"slug":"q3-plan-md","target":"~/Code/my-app/docs/q3-plan.md","created":"2026-11-19T19:09:24Z","origin":"sl-link","project":"~/Code/my-app","session":"0dae59d5-1e9f-4136-9416-7534ce4a461c","linked":3,"last_linked":"2026-11-20T09:12:01Z","clicks":2,"last_clicked":"2026-11-20T09:15:40Z"}
```

### Per click (`clicks.jsonl`, append-only)

| Field | Example | Why |
|---|---|---|
| `t` | timestamp | |
| `slug` | `q3-plan-md` | Also logged for links in `links.txt`, and for unknown slugs |
| `device` | `100.101.102.103` | Name shown from `tailscale status` |
| `client` | `mac`, `phone`, `other` | Already worked out for routing |
| `route` | `open`, `finder`, `phone-page`, `serve`, `redirect`, `missing`, `unknown` | What the click actually did |
| `ref` | `https://www.notion.so` | Origin only, never the full URL |

Volume is small (a few dozen clicks a day, under 1 MB a year), so the log can
keep a year of raw clicks before folding older ones into the per-link counts.
`unknown` clicks (slugs that don't exist) show whether archiving or pruning
ever broke a link someone still used.

### Retention

- Archive an sl-link link 30 days after it was made if nobody clicked it, or
  180 days after its last click. Linking the same file again resets the clock.
- **An archived link keeps working.** Clicking one whose file still exists
  opens the file and brings the link back. Archiving only hides a link from
  `ls`, the manage page and the menu.
- **Slugs are never reused.** A removed link keeps a tombstone. Clicking it
  shows "this file no longer exists" with the old path, never a different file.
- To keep an sl-link link forever, name it. That moves it to `links.txt`, as it
  does today.
- The 30 and 180 days are starting guesses. After a month of click data,
  `clicks.jsonl` answers what they should be: how many links are never
  clicked, and how long after creation the clicks come.

### What it makes possible

- **Manage page:** sort by recent or most clicked; show the project, the note,
  "never clicked" or "last opened 3 days ago from iPhone", and a way to open the
  session that made a link.
- **Commands:** `shortlinker ls --project my-app`, `--unclicked`, `--older 30d`.
- **Analysis:** DuckDB reads both files directly, for example
  `SELECT route, count(*) FROM read_json('clicks.jsonl') GROUP BY route`.

## Not recording

| Considered | Why not |
|---|---|
| Full user agent, full referrer, query strings | Nothing would use them, and they add fingerprinting detail |
| Location from the IP | Every client is on the tailnet; it says nothing |
| Tailscale login on each click | The same value on a one-person tailnet |
| Git commit at link time, to recover deleted files | Of 81 dead worktree links on the hub above, only 10 were in any commit; the rest were never committed |
| File inode, to follow moved files | Moves are rare: 79 of those 81 died because the whole worktree was removed, and nothing survives that |
| Page titles fetched for URL links | Needs network calls; the target is already readable |
| File size or contents | Not needed for any of the uses above |

## Privacy

Both files live in `~/.config/shortlinker/` and are readable only by you, like
`links.txt` and the token. The repository documents the fields; it never holds
anyone's data.

## Moving existing links

Existing sl-link links move into `auto-links.jsonl` with every slug, target and
phone page unchanged, the same way the split was verified. Their creation time
is unknown, so it's set to the moment of the move. That starts every 30-day
clock then, and nothing gets archived on the first day.
