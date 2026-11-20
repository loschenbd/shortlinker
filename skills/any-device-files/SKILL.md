---
name: any-device-files
description: |
  Set up a Mac so that everything a coding agent makes (screenshots, videos, PDFs,
  Markdown plans, code) opens on the owner's phone too, and Markdown and code can be
  edited there and saved straight back to the Mac. Uses Tailscale (a private network),
  shortlinker (a link for any file), SilverBullet (a Markdown and code editor the Mac
  serves to the phone's browser) and an instruction that has the agent hand over links
  instead of paths. Use when someone wants to open or edit their agent's files from
  their phone or another device, or points you at this guide.
---

# Open and edit your agent's files from any device

You are setting this up on the user's Mac, where their agent works, and on their phone:

1. **A private network.** Tailscale on both devices.
2. **A link for any file.** shortlinker turns a path into a link. On the Mac it opens
   the file in its own app; on the phone it opens in the browser.
3. **An editor for Markdown and code.** SilverBullet runs on the Mac. The phone opens
   it in the browser, and edits land in the files on the Mac, so the agent sees them.
4. **An instruction for the agent**, so it gives the user links instead of paths.
5. **Dev servers** the phone can open at the Mac's private address.

Nothing here is reachable from the internet. Only devices signed in to the user's
Tailscale account can reach any of it.

## Ground rules

- Go one step at a time. Check what's already installed and skip what's done.
  Before each install or system change, show the command and ask.
- **The user does every sign-in and every password**: the Tailscale login, the
  SilverBullet admin account. Never ask for a password in the chat, never print one,
  and never put one in a file.
- Never run `tailscale funnel`. It publishes to the internet.
- Before any `tailscale serve`, run `tailscale serve status`. Always pass
  `--https=<port>`; a bare `tailscale serve --bg <port>` takes over port 443. Never
  replace a port that's already mapped: stop and ask.
- Services listen on `127.0.0.1` only. `tailscale serve` is the one way in.
- This guide is for macOS. A Linux server is covered at the end.
- It is new (November 2026) and follows the author's working setup, but hasn't yet been
  run end to end on a fresh Mac. If a step doesn't match what you see, stop, tell the
  user, and don't improvise around security settings.

## Step 1: Tailscale on the Mac and the phone

Check with `tailscale status`. The CLI is `/usr/local/bin/tailscale` for the standalone
app once "CLI integration" is on in its settings, or
`/Applications/Tailscale.app/Contents/MacOS/Tailscale` for the App Store app.

If it's missing, the user installs it and signs in:

- **Mac:** https://tailscale.com/download
- **Phone:** the Tailscale app, signed in to the same account.
- **Admin console → DNS:** turn on MagicDNS, then HTTPS Certificates
  (https://tailscale.com/kb/1153/enabling-https). `tailscale serve` needs them.

**Check:** `tailscale status` lists both devices. Note the Mac's name
(`tailscale status --json`, field `Self.DNSName` without its final dot, such as
`mac.tail1234.ts.net`) and
its address (`tailscale ip -4`, a `100.x.y.z`).

If the phone also runs another VPN or an exit node and `*.ts.net` names don't load,
turn it off to test. Plain `http://100.x.y.z:<port>` addresses usually still work.

## Step 2: shortlinker, a link for any file

It needs Go (`go version`; with the user's OK, `brew install go`). Ask where they keep
code, then:

```sh
git clone https://github.com/loschenbd/shortlinker <code folder>/shortlinker
cd <code folder>/shortlinker && make install
```

That installs `shortlinker` and `sl-link` in `~/.local/bin` (add it to `PATH` if it
isn't) and a LaunchAgent that starts the hub at login. Publish port 7777 to the
tailnet. **Never publish 7778**: it opens files on the Mac and holds the link API.

```sh
tailscale serve status
tailscale serve --bg --https=443 http://127.0.0.1:7777   # only if 443 is free
```

Suggest the user keep the Mac from sleeping while it's plugged in (System Settings →
Battery or Energy). Links only work while it's awake.

**Check:** run `sl-link <an image on the Mac>`. It prints
`https://<mac name>/<slug>`. The user opens it on the phone and sees the image. On the
Mac, the same link opens the image in its app. Try a video and a PDF too.

## Step 3: SilverBullet, to edit Markdown and code on the phone

**Install the server.** Download the `silverbullet-server-darwin-*` zip that matches
`uname -m` from https://github.com/silverbulletmd/silverbullet/releases/latest. Check
its SHA-256 against the asset's `digest` in the GitHub API
(`/repos/silverbulletmd/silverbullet/releases/latest`), unzip it and install the
`silverbullet` binary in `~/.local/bin`. Version 2.12.1 was current in November 2026.

**Run it at login, on loopback, with shell commands off.** Pick a free port
(`lsof -nP -iTCP:3010 -sTCP:LISTEN` prints nothing). Write
`~/Library/LaunchAgents/com.<user>.silverbullet.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.USER.silverbullet</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/USER/.local/bin/silverbullet</string>
    <string>-L</string><string>127.0.0.1</string>
    <string>-p</string><string>3010</string>
    <string>/Users/USER/.local/share/silverbullet</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SB_SHELL_BACKEND</key><string>off</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Users/USER/Library/Logs/silverbullet.log</string>
  <key>StandardErrorPath</key><string>/Users/USER/Library/Logs/silverbullet.log</string>
</dict>
</plist>
```

`SB_SHELL_BACKEND=off` matters. A SilverBullet space can be set to run shell commands
on the Mac from the browser, and this blocks that for the whole server. Create the
empty data folder (`mkdir -p ~/.local/share/silverbullet`), then
`launchctl bootstrap gui/$(id -u) <plist>`.

**First run (the user does this).** They open `http://localhost:3010` on the Mac.
SilverBullet starts in setup mode. They create the admin account, keeping the
password in their password manager, and then the first space.

**One space per folder they want on the phone.** In the Dashboard, each space has a
folder (an existing project folder, such as `~/Projects/app`) and a URL prefix (a
short name, such as `app`). SilverBullet shows Markdown as pages and opens code files
in its text editor. It skips dot-folders.

**Publish it to the tailnet:**

```sh
tailscale serve status
tailscale serve --bg --https=8443 http://127.0.0.1:3010   # or another free port
```

**Check:** on the phone, open `https://<mac name>:8443`, log in, open a Markdown page,
change a line, and confirm on the Mac (`git diff` or `cat`) that the file changed.

**Connect it to shortlinker.** When `sl-link` links a Markdown or code file, the hub
runs a command named `sb-link` to get the file's SilverBullet address, and phones then
open the link in the editor. This guide ships that command: copy
`skills/any-device-files/scripts/sb-link` from the shortlinker clone (Step 2) to
`~/.local/bin/sb-link` and make it executable. Then write `~/.config/sb-link/spaces`
with one line per space:

```
url=https://<mac name>:8443
app=/Users/USER/Projects/app
notes=/Users/USER/Projects/notes
```

**Check:** `sb-link ~/Projects/app/README.md` prints `https://<mac name>:8443/app/README`.
Then `sl-link` that file. The user opens the link on the phone and lands in the editor.
For files linked before this step, `shortlinker phone-pages` fills them in.

## Step 4: Tell the agent

Add this to the agent's global instructions (Claude Code: `~/.claude/CLAUDE.md`;
Codex: `~/.codex/AGENTS.md`; others: their rules file). Show it to the user before
writing it:

```markdown
## Files and links
When you point me to a file, run `sl-link <absolute path>` and give me the link it
prints as a Markdown link with a short name. It opens on my laptop or my phone.
Markdown and code links open in SilverBullet, where I can edit them.
When you start a dev server, also give me its address on my private network:
http://<tailnet address>:<port>.
```

## Step 5: Dev servers

Most dev servers listen on `localhost` only. To open one on the phone, it has to
listen on the Mac's Tailscale address. Bind to that address where the tool allows it,
for example Vite's `--host 100.x.y.z` or `next dev -H 100.x.y.z`. Binding to `0.0.0.0`
also works, but then any Wi-Fi network the Mac joins can reach it too. Some frameworks
also check the request's origin; for example, Next.js 15.2+ needs the address in
`allowedDevOrigins`. Open `http://100.x.y.z:<port>` on the phone.

Don't `tailscale serve` a dev server's own port; the plain address stops answering.
If the phone needs HTTPS (service workers, camera, clipboard), serve it on a different
port, such as `tailscale serve --bg --https=8444 http://127.0.0.1:<port>`, and remove
that mapping afterwards with `tailscale serve --https=8444 off`.

## Step 6 (optional): sessions on the phone

Most people already reach their agent from the phone. If the user doesn't, the usual
pieces are SSH over Tailscale, a terminal multiplexer so sessions survive
disconnects (tmux, herdr), and a terminal app on the phone (Moshi, Blink, Termius).

## Final check

From the phone, open a link to an image, a video, a PDF and a Markdown file (edit it
and save), and a running dev server. Tell the user what worked and what didn't.

## On a Linux server instead

The same setup can run on a cloud server with the user's repos cloned there.

- **Tailscale:** `curl -fsSL https://tailscale.com/install.sh | sh`, then
  `sudo tailscale up`, and the user follows the login link
  (https://tailscale.com/kb/1031/install-linux).
- **SilverBullet:** the `silverbullet-server-linux-*` zip, run by a systemd service
  instead of the LaunchAgent, with the same flags and `SB_SHELL_BACKEND=off`.
- **shortlinker:** its installer is macOS-only (launchd). `GOOS=linux go build` works
  and phones get files through its links, but you write the systemd unit yourself, and
  a laptop opens links in the browser, like the phone, rather than in an app.

## Undo

- `tailscale serve --https=443 off` and `tailscale serve --https=8443 off`
- `launchctl bootout gui/$(id -u)/com.<user>.silverbullet` and
  `launchctl bootout gui/$(id -u)/com.<user>.shortlinker`, then delete both plists
  from `~/Library/LaunchAgents`. shortlinker has no uninstall target.
- Remove `~/.local/bin/sb-link` and the "Files and links" section from the agent's
  instructions.
