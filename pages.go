package main

// openedPage is returned by the opener after a successful open.
// Browsers won't auto-close a tab they didn't script-open; window.close()
// is best-effort and the fallback text tells the user to close it.
const openedPage = `<!doctype html>
<meta charset="utf-8"><title>shortlinker</title>
<body style="font-family:-apple-system,sans-serif;text-align:center;padding-top:20vh;color:#333">
<h1>&#10003; Opened</h1><p>You can close this tab.</p>
<script>window.close()</script>
`

// revealedPage is returned when the target is an app or script, which the
// opener shows in Finder rather than running.
const revealedPage = `<!doctype html>
<meta charset="utf-8"><title>shortlinker</title>
<body style="font-family:-apple-system,sans-serif;text-align:center;padding-top:20vh;color:#333">
<h1>Shown in Finder</h1><p>This link points to an app or a script, so it's shown in Finder instead of run.<br>You can close this tab.</p>
`

// shownPage is returned for a ?finder link, which shows the file in Finder.
const shownPage = `<!doctype html>
<meta charset="utf-8"><title>shortlinker</title>
<body style="font-family:-apple-system,sans-serif;text-align:center;padding-top:20vh;color:#333">
<h1>&#10003; Shown in Finder</h1><p>You can close this tab.</p>
<script>window.close()</script>
`

// listingPage renders a served folder. The %s values are the escaped title
// (twice) and the pre-escaped list items.
const listingPage = `<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s</title>
<body style="font-family:-apple-system,sans-serif;max-width:40rem;margin:0 auto;padding:16px;color:#333;line-height:1.6">
<h1 style="font-size:1.2rem;word-break:break-word">%s</h1><ul>
%s</ul>
`
