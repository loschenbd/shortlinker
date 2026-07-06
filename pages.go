package main

// openedPage is returned by the opener agent after a successful open.
// Browsers won't auto-close a tab they didn't script-open; window.close()
// is best-effort and the fallback text tells the user to close it.
const openedPage = `<!doctype html>
<meta charset="utf-8"><title>shortlinker</title>
<body style="font-family:-apple-system,sans-serif;text-align:center;padding-top:20vh;color:#333">
<h1>&#10003; Opened</h1><p>You can close this tab.</p>
<script>window.close()</script>
`

// macOnlyPage is shown to mobile clients for file/folder targets.
// The single %s is the slug (validated [a-z0-9-], so safe to interpolate).
const macOnlyPage = `<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>shortlinker</title>
<body style="font-family:-apple-system,sans-serif;text-align:center;padding-top:20vh;color:#333">
<h1>Mac-only link</h1><p>&ldquo;%s&rdquo; opens a file or folder on a Mac.<br>Open this link on your Mac instead.</p>
`
