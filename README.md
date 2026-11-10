# shortlinker

Self-hosted, tailnet-only short links. Paste `https://<hub>.<tailnet>.ts.net/budget`
into Notion; clicking it opens app deep links (`obsidian://…`) on Mac and iOS,
opens files/folders on whichever Mac you clicked, and serves them to phones.
One Go binary, zero dependencies, nothing leaves your Tailscale network.

Design: `docs/superpowers/specs/2026-07-06-shortlinker-design.md`

## Install

**Hub (the designated, usually-awake Mac):**

    make install
    # One-time: publish the hub at https://<hub>.<tailnet>.ts.net.
    # This replaces whatever this Mac already serves at / on port 443.
    tailscale serve --bg --https=443 http://127.0.0.1:7777

The hub listens on two loopback ports. **7777** is the public one: `tailscale
serve` publishes it, and it holds the short links and file serving. **7778**
opens files on this Mac and changes links. Never publish 7778.

Recommended: System Settings → Battery/Energy → prevent sleep when plugged in.
Every link requires the hub to be awake.

**Every other Mac (opener agent):**

    HUB=https://<hub>.<tailnet>.ts.net make install

An agent listens only on 7778, to open files on its own Mac.

## Adding links

Edit `~/.config/shortlinker/links.txt` on the hub (takes effect immediately,
no restart):

    # slug = target      (:// → redirect, otherwise file/folder path)
    budget = ~/Documents/Finance/2026-budget.numbers
    daily  = obsidian://daily?vault=Main
    proj   = ~/Code/my-app

Or through the API, which only answers on the hub Mac itself (port 7778):

    curl -X POST http://127.0.0.1:7778/api/links -H 'Content-Type: application/json' \
      -d '{"slug":"budget","target":"~/Documents/Finance/2026-budget.numbers"}'
    curl http://127.0.0.1:7778/api/links
    curl -X DELETE http://127.0.0.1:7778/api/links/budget

Slugs: lowercase letters, digits, hyphens. `o`, `f` and `api` are reserved.

## A link for any file: `sl-link`

    sl-link ~/Documents/report.pdf docs/notes.md
    # https://<hub>.<tailnet>.ts.net/report-pdf
    # https://<hub>.<tailnet>.ts.net/notes-md

Prints one link per file or folder, registering it if it's new. Run it on the
hub Mac. The slug is the file's name (`report-pdf`); when that's taken it
tries the folder plus the name (`documents-report-pdf`), then `-2`, `-3`. Linking the
same path again prints the same link. The link works everywhere: Ghostty and
Moshi Desktop open the file on the Mac (where a `file://` link is dead in
Moshi Desktop's chat view), and a phone shows it.

- Exit codes: 0 ok, 1 error, 2 no such path, 4 hub unreachable.
- The address it prints comes from `SHORTLINKER_URL`, else
  `~/.config/shortlinker/url`. The first run fills that file in from this
  Mac's tailnet name, which takes several seconds once: the macOS `tailscale`
  CLI starts the whole app on every call. If the Mac's tailnet name changes,
  delete the file.
- `sl-link` is a symlink to the binary; `shortlinker link <path>` does the
  same.

sl-link's links go in their own section at the end of `links.txt`, under a
`# Added by sl-link` line. Hand-made links stay above it, including ones
added through the API with a slug. To clear out links whose file is gone
(scratch files, renamed docs):

    shortlinker prune -n     # list them
    shortlinker prune        # remove them

It only touches the sl-link section. A removed slug can be given to a
different file later, so an old link to a deleted file may then show the new
one.

The API does the same as `sl-link` when you leave out the slug:

    curl -X POST http://127.0.0.1:7778/api/links -H 'Content-Type: application/json' \
      -d '{"target":"~/Documents/report.pdf"}'
    # {"created":true,"slug":"report-pdf","target":"~/Documents/report.pdf"}

## How a click works

- Deep link / URL target → 302 straight to the target (Mac + iOS).
- File/folder target → 302 to `http://localhost:7778/o/<slug>`, handled by
  the clicking Mac's own opener, which runs `open`. Apps, scripts, installers
  and other executable files are shown in Finder instead of opened.
  (Note: iPads using desktop-class Safari report a Mac user agent and will see
  a connection error instead.)
- File/folder target on a phone → 302 to `/f/<slug>`, where the hub serves
  the file itself (HTML renders, with `charset=utf-8`). A folder serves the
  files beneath it, `index.html` if there is one, or a listing. Markdown,
  scripts and other text files show as plain text. Any device can open
  `/f/<slug>` directly to read a file instead of opening it.

## Security model

- Binds `127.0.0.1` only; the sole network path in is `tailscale serve`
  (WireGuard-authenticated tailnet devices, never the public internet), and
  it only reaches port 7777.
- **Why two ports:** every request through `tailscale serve` arrives from
  127.0.0.1 carrying whatever Host header the sender chose, so nothing on
  7777 can tell this Mac from another tailnet device. Opening files and
  changing links therefore live on 7778, which the tailnet can't reach.
  Before this split, a tailnet device could register any path (say
  `~/.ssh/id_ed25519`) and read it back through `/f/`, or forge
  `Host: localhost` to make the Mac run `open`.
- 7778 refuses any Host but `localhost` and `127.0.0.1` (DNS rebinding), and
  its write endpoints refuse requests a web page sends (any `Sec-Fetch-Site`
  other than `none`, or a non-JSON body). curl and agents are unaffected.
- `open` only ever runs on targets of registered slugs. Any web page can send
  your browser to `localhost:7778/o/<slug>`, so targets that would run code
  are shown in Finder rather than opened.
- `/f/` serves only registered file/folder slugs, to any device on your
  tailnet. Inside a folder slug, `os.Root` refuses `..` and symlinks that leave
  the folder, and dot-paths (`.git`, `.env`) are never served or listed.
  Requests must arrive as `localhost`, `127.0.0.1` or a `*.ts.net` name.
  Every response carries `X-Content-Type-Options: nosniff`.
- Served pages share one origin, so a page that runs script could read every
  other served file. HTML, SVG and XML from sl-link's links, and anything
  inside a folder link, are served with `Content-Security-Policy: sandbox`
  (scripts run, but in an opaque origin with no storage). A file linked by
  hand is trusted and keeps a normal origin, so pages that save settings in
  `localStorage` still work. PDFs and images are never sandboxed (Chrome
  won't show a sandboxed PDF).
- `links.txt` is replaced atomically (temporary file, then rename), so a
  click during a save or a crash never sees half a file.

## Troubleshooting

- Logs: `~/Library/Logs/shortlinker.log`
- Restart: `launchctl unload ~/Library/LaunchAgents/com.$(id -un).shortlinker.plist && launchctl load ~/Library/LaunchAgents/com.$(id -un).shortlinker.plist`
- "hub unreachable — is it awake?": wake the hub Mac.
- `sl-link` exits 4: the hub isn't running on this Mac; check the log.
