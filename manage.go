package main

// managePage is the manage page on the local port: search, copy, open,
// edit and delete links. It holds no secrets: it signs in by trading the
// one-time code in its #fragment (from `shortlinker manage`) for a session,
// and sends that with every API call. Link data only ever reaches the page
// through textContent, never as HTML. (No backticks below: this is a Go raw
// string.)
const managePage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>shortlinker</title>
<style>
:root {
  --bg: #fafaf9; --panel: #ffffff; --ink: #1c1c1a; --muted: #6b6a66; --faint: #9c9a95;
  --line: #e7e5e1; --hover: #f3f2ef; --accent: #2f5fd0; --accent-ink: #ffffff;
  --warn: #a4471d; --warn-bg: #fbeee6; --tag: #efeeea; --focus: rgba(47,95,208,.35);
  color-scheme: light;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #161615; --panel: #1e1e1c; --ink: #ecebe8; --muted: #a3a19c; --faint: #75736e;
    --line: #2e2d2a; --hover: #262624; --accent: #7fa2ff; --accent-ink: #10131a;
    --warn: #f0a07a; --warn-bg: #3a2419; --tag: #2a2927; --focus: rgba(127,162,255,.35);
    color-scheme: dark;
  }
}
* { box-sizing: border-box; }
[hidden] { display: none !important; }
body { margin: 0; background: var(--bg); color: var(--ink); font: 14px/1.45 -apple-system, BlinkMacSystemFont, "Helvetica Neue", sans-serif; }
main { max-width: 920px; margin: 0 auto; padding: 24px 16px 64px; }
header { display: flex; align-items: baseline; gap: 12px; margin-bottom: 16px; }
h1 { font-size: 17px; font-weight: 600; margin: 0; letter-spacing: -.01em; }
#count { color: var(--muted); font-variant-numeric: tabular-nums; }
header .spacer { flex: 1; }
.bar { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; margin-bottom: 12px; }
#q { flex: 1 1 320px; min-width: 0; font: inherit; font-size: 15px; padding: 9px 12px; border: 1px solid var(--line); border-radius: 8px; background: var(--panel); color: var(--ink); }
#q:focus, button:focus-visible, input:focus { outline: none; box-shadow: 0 0 0 3px var(--focus); border-color: var(--accent); }
.seg { display: inline-flex; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; background: var(--panel); }
.seg button { border: 0; border-radius: 0; background: none; padding: 8px 11px; color: var(--muted); }
.seg button[aria-pressed="true"] { background: var(--tag); color: var(--ink); font-weight: 600; }
button { font: inherit; cursor: pointer; border: 1px solid var(--line); background: var(--panel); color: var(--ink); border-radius: 7px; padding: 6px 10px; }
button:hover { background: var(--hover); }
button.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); font-weight: 600; }
button.danger { color: var(--warn); }
ul { list-style: none; margin: 0; padding: 0; background: var(--panel); border: 1px solid var(--line); border-radius: 10px; }
li { display: flex; gap: 12px; align-items: center; padding: 9px 12px; border-top: 1px solid var(--line); }
li:first-child { border-top: 0; }
li:hover { background: var(--hover); }
.what { flex: 1; min-width: 0; }
.slug { font: 600 13px/1.3 ui-monospace, "SF Mono", Menlo, monospace; word-break: break-all; }
.target { color: var(--muted); font: 12px/1.4 ui-monospace, "SF Mono", Menlo, monospace; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tags { display: inline-flex; gap: 4px; margin-left: 6px; vertical-align: 1px; }
.tag { font: 500 11px/1 -apple-system, sans-serif; padding: 3px 6px; border-radius: 5px; background: var(--tag); color: var(--muted); }
.tag.missing { background: var(--warn-bg); color: var(--warn); }
.acts { display: flex; gap: 4px; flex-shrink: 0; }
.acts button { padding: 4px 8px; font-size: 13px; }
.empty, .locked { padding: 40px 16px; text-align: center; color: var(--muted); }
.locked code { background: var(--tag); padding: 2px 6px; border-radius: 5px; color: var(--ink); }
#more { display: block; margin: 12px auto 0; }
#toast { position: fixed; left: 50%; bottom: 24px; transform: translateX(-50%); background: var(--ink); color: var(--bg); padding: 8px 14px; border-radius: 8px; opacity: 0; transition: opacity .15s; pointer-events: none; max-width: calc(100% - 32px); }
#toast.show { opacity: 1; }
dialog { border: 1px solid var(--line); border-radius: 12px; background: var(--panel); color: var(--ink); padding: 20px; width: min(520px, calc(100% - 32px)); }
dialog::backdrop { background: rgba(0,0,0,.35); }
dialog h2 { margin: 0 0 14px; font-size: 16px; }
dialog label { display: block; font-size: 12px; color: var(--muted); margin: 12px 0 4px; }
dialog input { width: 100%; font: 13px ui-monospace, "SF Mono", Menlo, monospace; padding: 8px 10px; border: 1px solid var(--line); border-radius: 7px; background: var(--bg); color: var(--ink); }
dialog .note { font-size: 12px; color: var(--muted); margin: 10px 0 0; min-height: 1em; }
dialog .note.err { color: var(--warn); }
dialog .row { display: flex; justify-content: flex-end; gap: 8px; margin-top: 18px; }
kbd { font: 11px ui-monospace, Menlo, monospace; border: 1px solid var(--line); border-bottom-width: 2px; border-radius: 4px; padding: 0 4px; color: var(--muted); }
.hint { color: var(--faint); font-size: 12px; margin: 10px 2px 0; }
@media (max-width: 600px) { li { flex-wrap: wrap; } .acts { width: 100%; justify-content: flex-end; } }
</style>
</head>
<body>
<main>
  <header><h1>shortlinker</h1><span id="count"></span><span class="spacer"></span><button id="new" class="primary" hidden>New link</button></header>
  <div id="app" hidden>
    <div class="bar">
      <input id="q" type="search" placeholder="Search links" autocomplete="off" spellcheck="false" aria-label="Search links">
      <div class="seg" role="group" aria-label="Show">
        <button data-f="all" aria-pressed="true">All</button>
        <button data-f="yours" aria-pressed="false">Yours</button>
        <button data-f="sl-link" aria-pressed="false">sl-link</button>
        <button data-f="missing" aria-pressed="false">Missing</button>
      </div>
    </div>
    <ul id="list"></ul>
    <button id="more" hidden></button>
    <p class="hint"><kbd>/</kbd> search · <kbd>Enter</kbd> copies the first link · <kbd>Esc</kbd> clears</p>
  </div>
  <div id="locked" class="locked" hidden>
    <p>Open this page from the menu bar (<b>Find links…</b>) or run <code>shortlinker manage</code> on the hub Mac.</p>
  </div>
</main>
<div id="toast" role="status" aria-live="polite"></div>
<dialog id="edit">
  <form method="dialog">
    <h2 id="edit-title">Edit link</h2>
    <label for="f-slug">Slug (lowercase letters, digits and hyphens)</label>
    <input id="f-slug" required pattern="[a-z0-9-]+" autocomplete="off" spellcheck="false">
    <label for="f-target">Target (a path, or a URL with ://)</label>
    <input id="f-target" required autocomplete="off" spellcheck="false">
    <p class="note" id="f-note"></p>
    <div class="row"><button value="cancel" formnovalidate>Cancel</button><button id="f-save" class="primary" value="save">Save</button></div>
  </form>
</dialog>
<script>
"use strict";
var KEY = "shortlinker-session";
var session = null, hub = "", links = [], filter = "all", limit = 200, editing = null;
var $ = function (id) { return document.getElementById(id); };

function toast(msg) {
  var t = $("toast");
  t.textContent = msg;
  t.classList.add("show");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(function () { t.classList.remove("show"); }, 1800);
}

function locked() {
  try { localStorage.removeItem(KEY); } catch (e) {}
  $("app").hidden = true; $("new").hidden = true; $("locked").hidden = false; $("count").textContent = "";
}

function api(method, path, body) {
  var opts = { method: method, headers: { "Authorization": "Bearer " + session } };
  if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  return fetch(path, opts).then(function (r) {
    if (r.status === 401) { locked(); throw new Error("Signed out"); }
    return r.json().catch(function () { return {}; }).then(function (j) {
      if (!r.ok) throw new Error(j.error || r.statusText);
      return j;
    });
  });
}

function signIn() {
  var code = new URLSearchParams(location.hash.slice(1)).get("code");
  if (code) history.replaceState(null, "", location.pathname);
  try {
    var saved = JSON.parse(localStorage.getItem(KEY) || "null");
    if (saved && saved.expires * 1000 > Date.now()) session = saved.session;
  } catch (e) {}
  if (!code) return Promise.resolve(!!session);
  return fetch("/api/session", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ code: code }) })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (j) {
      if (j) {
        session = j.session;
        try { localStorage.setItem(KEY, JSON.stringify(j)); } catch (e) {}
      }
      return !!session;
    });
}

function load() {
  return api("GET", "/api/links?details=1").then(function (j) {
    hub = j.hub || ""; links = j.links || [];
    $("locked").hidden = true; $("app").hidden = false; $("new").hidden = false;
    render();
  });
}

function visible() {
  var words = $("q").value.toLowerCase().split(/\s+/).filter(Boolean);
  return links.filter(function (l) {
    if (filter === "yours" && l.source !== "yours") return false;
    if (filter === "sl-link" && l.source !== "sl-link") return false;
    if (filter === "missing" && l.kind !== "missing") return false;
    var hay = (l.slug + " " + l.target).toLowerCase();
    return words.every(function (w) { return hay.indexOf(w) >= 0; });
  });
}

function el(tag, cls, text) {
  var e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function button(label, title, fn, cls) {
  var b = el("button", cls, label);
  b.type = "button"; b.title = title;
  b.addEventListener("click", fn);
  return b;
}

function render() {
  var shown = visible(), list = $("list");
  var total = links.length, mine = links.filter(function (l) { return l.source === "yours"; }).length;
  $("count").textContent = total + " links · " + mine + " yours · " + (total - mine) + " from sl-link";
  list.textContent = "";
  if (!shown.length) {
    list.appendChild(el("li", "empty", links.length ? "No links match." : "No links yet."));
  }
  var frag = document.createDocumentFragment();
  shown.slice(0, limit).forEach(function (l) {
    var li = el("li"), what = el("div", "what"), top = el("div");
    top.appendChild(el("span", "slug", l.slug));
    var tags = el("span", "tags");
    if (l.source === "sl-link") tags.appendChild(el("span", "tag", "sl-link"));
    if (l.phone) tags.appendChild(el("span", "tag", "phone page"));
    if (l.kind === "missing") tags.appendChild(el("span", "tag missing", "missing"));
    if (l.kind === "folder") tags.appendChild(el("span", "tag", "folder"));
    top.appendChild(tags);
    what.appendChild(top);
    var t = el("div", "target", l.target); t.title = l.target;
    what.appendChild(t);
    li.appendChild(what);
    var acts = el("div", "acts");
    acts.appendChild(button("Copy", "Copy the link", function () { copy(l); }));
    if (l.kind === "url") acts.appendChild(button("Open", "Open the link", function () { window.open(l.target, "_blank", "noopener"); }));
    if (l.kind === "file" || l.kind === "folder") {
      acts.appendChild(button("Open", "Open on this Mac", function () { openOnMac(l, false); }));
      acts.appendChild(button("Finder", "Show in Finder", function () { openOnMac(l, true); }));
    }
    acts.appendChild(button("Edit", "Rename it or change what it points to", function () { edit(l); }));
    acts.appendChild(button("Delete", "Delete the link", function () { remove(l); }, "danger"));
    li.appendChild(acts);
    frag.appendChild(li);
  });
  list.appendChild(frag);
  var more = $("more");
  more.hidden = shown.length <= limit;
  more.textContent = "Show all " + shown.length;
}

function copy(l) {
  if (!hub) { toast("The hub's address isn't known yet: run sl-link once"); return; }
  navigator.clipboard.writeText(hub + "/" + l.slug).then(
    function () { toast("Copied " + hub.replace(/^https?:\/\//, "") + "/" + l.slug); },
    function () { toast("Couldn't copy"); });
}

function openOnMac(l, reveal) {
  fetch("/o/" + encodeURIComponent(l.slug) + (reveal ? "?reveal=1" : "")).then(function (r) {
    if (r.ok) toast(reveal ? "Shown in Finder" : "Opened");
    else r.text().then(function (t) { toast(t.trim() || r.statusText); });
  });
}

function remove(l) {
  if (!confirm("Delete " + l.slug + "? Links to it will stop working.")) return;
  api("DELETE", "/api/links/" + encodeURIComponent(l.slug)).then(function () { toast("Deleted " + l.slug); return load(); },
    function (e) { toast(e.message); });
}

function edit(l) {
  editing = l;
  $("edit-title").textContent = l ? "Edit " + l.slug : "New link";
  $("f-slug").value = l ? l.slug : "";
  $("f-target").value = l ? l.target : "";
  var note = $("f-note");
  note.className = "note";
  note.textContent = l && l.source === "sl-link" ? "Saving a new target makes this one of your links (in links.txt)." : "";
  $("edit").showModal();
  (l ? $("f-target") : $("f-slug")).focus();
}

$("edit").addEventListener("close", function () {
  if ($("edit").returnValue !== "save") return;
  var slug = $("f-slug").value.trim(), target = $("f-target").value.trim(), l = editing;
  var step = Promise.resolve();
  if (l && slug !== l.slug) {
    if (!confirm("Rename " + l.slug + " to " + slug + "? Links to " + l.slug + " will stop working.")) return;
    step = step.then(function () { return api("POST", "/api/links/" + encodeURIComponent(l.slug) + "/rename", { to: slug }); });
  }
  if (!l || target !== l.target) step = step.then(function () { return api("POST", "/api/links", { slug: slug, target: target }); });
  step.then(function () { toast("Saved " + slug); return load(); }, function (e) {
    toast(e.message);
    if (l && slug !== l.slug) load();
  });
});

document.querySelectorAll(".seg button").forEach(function (b) {
  b.addEventListener("click", function () {
    filter = b.dataset.f; limit = 200;
    document.querySelectorAll(".seg button").forEach(function (o) { o.setAttribute("aria-pressed", String(o === b)); });
    render();
  });
});
$("q").addEventListener("input", function () { limit = 200; render(); });
$("q").addEventListener("keydown", function (e) {
  if (e.key === "Enter") { var first = visible()[0]; if (first) copy(first); }
  if (e.key === "Escape") { $("q").value = ""; render(); }
});
$("more").addEventListener("click", function () { limit = Infinity; render(); });
$("new").addEventListener("click", function () { edit(null); });
document.addEventListener("keydown", function (e) {
  if (e.key === "/" && document.activeElement !== $("q") && !$("edit").open) { e.preventDefault(); $("q").focus(); }
});

signIn().then(function (ok) { return ok ? load() : locked(); }).catch(function (e) { if (e.message !== "Signed out") toast(e.message); });
$("q").focus();
</script>
</body>
</html>
`
