# shortlinker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single Go binary that serves tailnet-only short links which open app deep links everywhere and local files/folders on whichever Mac they're clicked on.

**Architecture:** One binary, two roles. The hub (designated Mac) owns `links.txt` and serves canonical URLs plus a JSON API; every Mac also runs the same binary as an opener agent on `127.0.0.1:7777` that turns a slug into a local `/usr/bin/open` call. Agents resolve slugs by asking the hub — no file syncing. See `docs/superpowers/specs/2026-07-06-shortlinker-design.md`.

**Tech Stack:** Go ≥1.22 (needs `http.ServeMux` method/wildcard patterns like `GET /o/{slug}` and `r.PathValue`), standard library ONLY — no third-party dependencies. macOS launchd for process management, `tailscale serve` for network exposure.

## Global Constraints

- Standard library only. Adding any dependency to `go.mod` is a plan violation.
- The server binds `127.0.0.1:<port>` only, never `0.0.0.0`. Default port: `7777`.
- Slugs match `^[a-z0-9-]+$`. Reserved (unregistrable) slugs: `o`, `api`.
- Links file default path: `~/.config/shortlinker/links.txt`. It is re-read on every request — no caching.
- `open` is only ever invoked with a target resolved from a registered slug; no request-supplied path/URL is ever passed to `open`. Invoke via `exec.Command` (no shell).
- All source files are `package main` at the repo root (flat layout — this is a ~600-line program; no internal packages).
- Run `go test ./...` before every commit; all tests must pass.
- Commit messages: conventional style (`feat:`, `test:`, `docs:`, `chore:`), each ending with the trailer `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.

---

### Task 1: Module scaffold + link-line parsing

**Files:**
- Create: `go.mod`, `.gitignore`
- Create: `links.go`
- Test: `links_test.go`

**Interfaces:**
- Consumes: nothing (first task)
- Produces (later tasks call these exact signatures):
  - `func ValidSlug(s string) bool`
  - `func IsRedirect(target string) bool` — true iff target contains `://`
  - `func ExpandPath(target, home string) string` — expands leading `~`
  - `func ParseLinks(content string) (map[string]string, []string)` — returns slug→target plus human-readable warnings for skipped lines

- [ ] **Step 1: Initialize the module**

```bash
cd /Users/alex/Code/shortlinker/.worktrees/shortlinker-initial
go mod init github.com/loschenbd/shortlinker
```

Create `.gitignore`:

```
bin/
```

- [ ] **Step 2: Write the failing tests**

Create `links_test.go`:

```go
package main

import "testing"

func TestParseLinks(t *testing.T) {
	content := `
# comment
budget = ~/Documents/Finance/2026-budget.numbers
daily  = obsidian://daily?vault=Main

bad line without equals
UPPER = /tmp/x
empty-target =
o = /tmp/reserved
`
	links, warnings := ParseLinks(content)
	want := map[string]string{
		"budget": "~/Documents/Finance/2026-budget.numbers",
		"daily":  "obsidian://daily?vault=Main",
	}
	if len(links) != len(want) {
		t.Fatalf("got %d links (%v), want %d", len(links), links, len(want))
	}
	for slug, target := range want {
		if links[slug] != target {
			t.Errorf("links[%q] = %q, want %q", slug, links[slug], target)
		}
	}
	if len(warnings) != 4 {
		t.Errorf("got %d warnings (%v), want 4", len(warnings), warnings)
	}
}

func TestValidSlug(t *testing.T) {
	cases := map[string]bool{
		"budget":    true,
		"a-1":       true,
		"":          false,
		"UPPER":     false,
		"has space": false,
		"o":         false, // reserved
		"api":       false, // reserved
	}
	for slug, want := range cases {
		if got := ValidSlug(slug); got != want {
			t.Errorf("ValidSlug(%q) = %v, want %v", slug, got, want)
		}
	}
}

func TestIsRedirect(t *testing.T) {
	cases := map[string]bool{
		"obsidian://daily?vault=Main":  true,
		"https://example.com/x":        true,
		"~/Documents/budget.numbers":   false,
		"/Users/alex/Desktop":          false,
	}
	for target, want := range cases {
		if got := IsRedirect(target); got != want {
			t.Errorf("IsRedirect(%q) = %v, want %v", target, got, want)
		}
	}
}

func TestExpandPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"~/Documents/x.pdf", "/Users/test/Documents/x.pdf"},
		{"~", "/Users/test"},
		{"/absolute/path", "/absolute/path"},
		{"https://example.com", "https://example.com"},
	}
	for _, c := range cases {
		if got := ExpandPath(c.in, "/Users/test"); got != c.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `undefined: ParseLinks`, `undefined: ValidSlug`, etc.

- [ ] **Step 4: Write the implementation**

Create `links.go`:

```go
package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var slugRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// reservedSlugs collide with routes and can never be registered.
var reservedSlugs = map[string]bool{"o": true, "api": true}

func ValidSlug(s string) bool {
	return slugRe.MatchString(s) && !reservedSlugs[s]
}

// IsRedirect reports whether target is served as a 302 redirect (anything
// with a URL scheme) rather than opened as a local path.
func IsRedirect(target string) bool {
	return strings.Contains(target, "://")
}

// ExpandPath replaces a leading ~ with home.
func ExpandPath(target, home string) string {
	if target == "~" {
		return home
	}
	if strings.HasPrefix(target, "~/") {
		return filepath.Join(home, target[2:])
	}
	return target
}

// ParseLinks parses links.txt content into slug→target. Blank lines and
// #-comments are ignored; malformed lines are skipped with a warning.
func ParseLinks(content string) (map[string]string, []string) {
	links := map[string]string{}
	var warnings []string
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		slug, target, ok := strings.Cut(trimmed, "=")
		slug, target = strings.TrimSpace(slug), strings.TrimSpace(target)
		if !ok || target == "" || !ValidSlug(slug) {
			warnings = append(warnings, fmt.Sprintf("line %d: skipping malformed line: %q", i+1, line))
			continue
		}
		links[slug] = target
	}
	return links, warnings
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./...`
Expected: `ok  github.com/loschenbd/shortlinker`

- [ ] **Step 6: Commit**

```bash
git add go.mod .gitignore links.go links_test.go
git commit -m "feat: link-line parsing, slug validation, path expansion

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: File-backed Store

**Files:**
- Create: `store.go`
- Test: `store_test.go`

**Interfaces:**
- Consumes: `ParseLinks`, `ValidSlug` (Task 1)
- Produces (later tasks call these exact signatures):
  - `type Store struct { Path string }`
  - `func (s *Store) All() (map[string]string, error)` — missing file ⇒ empty map, nil error
  - `func (s *Store) Get(slug string) (string, bool, error)`
  - `func (s *Store) Set(slug, target string) error` — updates matching line in place or appends; preserves comments, blank lines, ordering
  - `func (s *Store) Delete(slug string) error` — error if slug unknown
- Test helper produced for reuse: `func newTestStore(t *testing.T, content string) *Store`

- [ ] **Step 1: Write the failing tests**

Create `store_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T, content string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "links.txt")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Store{Path: path}
}

func TestStoreAllMissingFile(t *testing.T) {
	s := newTestStore(t, "")
	links, err := s.All()
	if err != nil {
		t.Fatalf("All() on missing file: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("links = %v, want empty", links)
	}
}

func TestStoreGet(t *testing.T) {
	s := newTestStore(t, "budget = ~/Documents/budget.numbers\n")
	target, ok, err := s.Get("budget")
	if err != nil || !ok || target != "~/Documents/budget.numbers" {
		t.Errorf("Get(budget) = %q, %v, %v", target, ok, err)
	}
	if _, ok, _ := s.Get("nope"); ok {
		t.Error("Get(nope) reported ok")
	}
}

func TestStoreSetAppendsNewSlug(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = /old\n")
	if err := s.Set("daily", "obsidian://daily"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# my links\nbudget = /old\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
}

func TestStoreSetUpdatesInPlacePreservingCommentsAndOrder(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = /old/path\ndaily = obsidian://daily\n")
	if err := s.Set("budget", "/new/path"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# my links\nbudget = /new/path\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
}

func TestStoreSetCreatesFileAndParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "links.txt")
	s := &Store{Path: path}
	if err := s.Set("budget", "/x"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "budget = /x\n" {
		t.Errorf("file = %q", data)
	}
}

func TestStoreSetRejectsInvalid(t *testing.T) {
	s := newTestStore(t, "")
	if err := s.Set("Bad Slug", "/x"); err == nil {
		t.Error("Set with invalid slug succeeded")
	}
	if err := s.Set("api", "/x"); err == nil {
		t.Error("Set with reserved slug succeeded")
	}
	if err := s.Set("ok", ""); err == nil {
		t.Error("Set with empty target succeeded")
	}
}

func TestStoreDelete(t *testing.T) {
	s := newTestStore(t, "# keep\nbudget = /x\ndaily = obsidian://daily\n")
	if err := s.Delete("budget"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# keep\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	if err := s.Delete("nope"); err == nil {
		t.Error("Delete(nope) succeeded, want error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `undefined: Store`

- [ ] **Step 3: Write the implementation**

Create `store.go`:

```go
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Store is a file-backed link store. Every read re-reads the file so edits
// made by hand or by other processes take effect immediately.
type Store struct {
	Path string
}

func (s *Store) All() (map[string]string, error) {
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	links, warnings := ParseLinks(string(data))
	for _, w := range warnings {
		log.Printf("%s: %s", s.Path, w)
	}
	return links, nil
}

func (s *Store) Get(slug string) (string, bool, error) {
	links, err := s.All()
	if err != nil {
		return "", false, err
	}
	target, ok := links[slug]
	return target, ok, nil
}

// Set adds or updates a link, preserving comments, blank lines, and the
// order of existing entries. New slugs are appended.
func (s *Store) Set(slug, target string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid slug %q (must match [a-z0-9-]+ and not be reserved)", slug)
	}
	if target == "" {
		return fmt.Errorf("empty target")
	}
	lines := s.readLines()
	newLine := slug + " = " + target
	replaced := false
	for i, line := range lines {
		if lineSlug(line) == slug {
			lines[i] = newLine
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, newLine)
	}
	return s.writeLines(lines)
}

func (s *Store) Delete(slug string) error {
	lines := s.readLines()
	kept := make([]string, 0, len(lines))
	found := false
	for _, line := range lines {
		if lineSlug(line) == slug {
			found = true
			continue
		}
		kept = append(kept, line)
	}
	if !found {
		return fmt.Errorf("unknown slug %q", slug)
	}
	return s.writeLines(kept)
}

// lineSlug returns the slug of a well-formed link line, or "".
func lineSlug(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	slug, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(slug)
}

func (s *Store) readLines() []string {
	data, err := os.ReadFile(s.Path)
	if err != nil || len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func (s *Store) writeLines(lines []string) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.Path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: `ok  github.com/loschenbd/shortlinker`

- [ ] **Step 5: Commit**

```bash
git add store.go store_test.go
git commit -m "feat: file-backed link store with comment-preserving writes

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Server skeleton + hub shortlink handler

**Files:**
- Create: `handlers.go`, `pages.go`
- Test: `handlers_test.go`

**Interfaces:**
- Consumes: `Store` (Task 2), `IsRedirect` (Task 1), `newTestStore` (Task 2 tests)
- Produces (later tasks rely on these exact signatures):
  - `type Server struct` with fields `store *Store`, `hubURL string`, `port string`, `home string`, `open func(target string) error`, `client *http.Client`
  - `func NewServer(store *Store, hubURL, port, home string) *Server` — `store == nil` means agent-only role
  - `func (s *Server) Handler() http.Handler`
  - `pages.go` constants: `openedPage`, `macOnlyPage` (has one `%s` verb for the slug)
- Test helper produced for reuse: `func newTestServer(t *testing.T, content string) (*Server, *[]string)` — returns server with `open` stubbed to record targets

- [ ] **Step 1: Write the failing tests**

Create `handlers_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testLinks = "budget = ~/Documents/budget.numbers\ndaily = obsidian://daily?vault=Main\n"

const desktopUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
const iphoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"

// newTestServer returns a hub-role server whose open func records targets
// instead of executing /usr/bin/open.
func newTestServer(t *testing.T, content string) (*Server, *[]string) {
	t.Helper()
	srv := NewServer(newTestStore(t, content), "", "7777", "/Users/test")
	opened := &[]string{}
	srv.open = func(target string) error {
		*opened = append(*opened, target)
		return nil
	}
	return srv, opened
}

func get(srv *Server, url, userAgent string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestShortlinkRedirectsSchemeTargets(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/daily", desktopUA)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "obsidian://daily?vault=Main" {
		t.Errorf("Location = %q", loc)
	}
}

func TestShortlinkBouncesFileTargetsToLocalhost(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/budget", desktopUA)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "http://localhost:7777/o/budget" {
		t.Errorf("Location = %q", loc)
	}
}

func TestShortlinkServesMacOnlyPageOnMobile(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/budget", iphoneUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Mac") {
		t.Errorf("body should mention Mac, got %q", rec.Body.String())
	}
}

func TestShortlinkUnknownSlug404(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/nope", desktopUA)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `undefined: NewServer`

- [ ] **Step 3: Write the implementation**

Create `pages.go`:

```go
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
```

Create `handlers.go`:

```go
package main

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type Server struct {
	store  *Store                    // nil on agent-only instances
	hubURL string                    // set on agent-only instances
	port   string                    // for Host checks and localhost bounce URLs
	home   string                    // for ~ expansion
	open   func(target string) error // injectable for tests
	client *http.Client              // for agent→hub resolve calls
}

func NewServer(store *Store, hubURL, port, home string) *Server {
	return &Server{
		store:  store,
		hubURL: strings.TrimRight(hubURL, "/"),
		port:   port,
		home:   home,
		open: func(target string) error {
			out, err := exec.Command("/usr/bin/open", target).CombinedOutput()
			if err != nil {
				return fmt.Errorf("open %q: %v: %s", target, err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		client: &http.Client{Timeout: 3 * time.Second},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if s.store != nil { // hub role
		mux.HandleFunc("GET /{slug}", s.handleShortlink)
	}
	return mux
}

func isMobile(userAgent string) bool {
	return strings.Contains(userAgent, "iPhone") ||
		strings.Contains(userAgent, "iPad") ||
		strings.Contains(userAgent, "Android")
}

// handleShortlink serves the canonical short URLs (hub role).
func (s *Server) handleShortlink(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	target, ok, err := s.store.Get(slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if IsRedirect(target) {
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	// File/folder target: mobile can't open it; Macs bounce to their own
	// local opener agent.
	if isMobile(r.UserAgent()) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, macOnlyPage, slug)
		return
	}
	http.Redirect(w, r, "http://localhost:"+s.port+"/o/"+slug, http.StatusFound)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: `ok  github.com/loschenbd/shortlinker`

- [ ] **Step 5: Commit**

```bash
git add handlers.go pages.go handlers_test.go
git commit -m "feat: hub shortlink handler with redirect/file/mobile branching

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Opener agent + resolve endpoint

**Files:**
- Modify: `handlers.go` (extend `Handler()`, add `handleOpen`, `resolve`, `handleResolve`, `writeJSON`)
- Test: `handlers_test.go` (append tests)

**Interfaces:**
- Consumes: `Server`, `newTestServer`, `get` helper (Task 3); `ExpandPath` (Task 1); `newTestStore` (Task 2)
- Produces:
  - Route `GET /o/{slug}` on ALL instances — Host-checked, opens locally
  - Route `GET /api/resolve/{slug}` on hub — `200 {"slug":…,"target":…}` or 404
  - `func writeJSON(w http.ResponseWriter, status int, v any)` — reused by Task 5

- [ ] **Step 1: Write the failing tests**

Append to `handlers_test.go`:

```go
func TestOpenRunsOpenOnRegisteredSlug(t *testing.T) {
	srv, opened := newTestServer(t, testLinks)
	rec := get(srv, "http://localhost:7777/o/budget", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if len(*opened) != 1 || (*opened)[0] != "/Users/test/Documents/budget.numbers" {
		t.Errorf("opened = %v, want expanded budget path", *opened)
	}
	if !strings.Contains(rec.Body.String(), "Opened") {
		t.Errorf("body = %q, want done page", rec.Body.String())
	}
}

func TestOpenRejectsForeignHost(t *testing.T) {
	srv, opened := newTestServer(t, testLinks)
	rec := get(srv, "http://evil.example:7777/o/budget", desktopUA)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(*opened) != 0 {
		t.Errorf("opened = %v, want nothing", *opened)
	}
}

func TestOpenUnknownSlug404(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://localhost:7777/o/nope", desktopUA)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestOpenReportsOpenFailure(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	srv.open = func(target string) error { return fmt.Errorf("open failed: no such file") }
	rec := get(srv, "http://localhost:7777/o/budget", desktopUA)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no such file") {
		t.Errorf("body = %q, want open error text", rec.Body.String())
	}
}

func TestResolveEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/api/resolve/budget", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct{ Slug, Target string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Slug != "budget" || body.Target != "~/Documents/budget.numbers" {
		t.Errorf("body = %+v", body)
	}
	if rec := get(srv, "http://hub.ts.example/api/resolve/nope", desktopUA); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", rec.Code)
	}
}

func TestAgentResolvesViaHub(t *testing.T) {
	hubSrv, _ := newTestServer(t, testLinks)
	hub := httptest.NewServer(hubSrv.Handler())
	defer hub.Close()

	agent := NewServer(nil, hub.URL, "7777", "/Users/test")
	opened := &[]string{}
	agent.open = func(target string) error {
		*opened = append(*opened, target)
		return nil
	}
	rec := get(agent, "http://localhost:7777/o/budget", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if len(*opened) != 1 || (*opened)[0] != "/Users/test/Documents/budget.numbers" {
		t.Errorf("opened = %v", *opened)
	}
}

func TestAgentHubUnreachable(t *testing.T) {
	agent := NewServer(nil, "http://127.0.0.1:1", "7777", "/Users/test")
	agent.open = func(target string) error { return nil }
	rec := get(agent, "http://localhost:7777/o/budget", desktopUA)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hub unreachable") {
		t.Errorf("body = %q, want hub-unreachable message", rec.Body.String())
	}
}
```

Add to the import block of `handlers_test.go`: `"encoding/json"` and `"fmt"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — `/o/budget` requests return 404 (route not registered yet)

- [ ] **Step 3: Write the implementation**

In `handlers.go`, replace `Handler()` with:

```go
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /o/{slug}", s.handleOpen)
	if s.store != nil { // hub role
		mux.HandleFunc("GET /{slug}", s.handleShortlink)
		mux.HandleFunc("GET /api/resolve/{slug}", s.handleResolve)
	}
	return mux
}
```

Append to `handlers.go`:

```go
// handleOpen runs `open` on a registered slug's target. Loopback clients
// only: the Host check blocks DNS-rebinding tricks.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	if host != "localhost" && host != "127.0.0.1" {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	slug := r.PathValue("slug")
	target, ok, err := s.resolve(slug)
	if err != nil {
		http.Error(w, "hub unreachable — is it awake? ("+err.Error()+")", http.StatusBadGateway)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.open(ExpandPath(target, s.home)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, openedPage)
}

// resolve looks a slug up locally (hub) or via the hub's API (agent).
func (s *Server) resolve(slug string) (string, bool, error) {
	if s.store != nil {
		return s.store.Get(slug)
	}
	resp, err := s.client.Get(s.hubURL + "/api/resolve/" + slug)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("hub returned %s", resp.Status)
	}
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, err
	}
	return body.Target, true, nil
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	target, ok, err := s.store.Get(slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "target": target})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
```

Add to the import block of `handlers.go`: `"encoding/json"` and `"net"`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: `ok  github.com/loschenbd/shortlinker`

- [ ] **Step 5: Commit**

```bash
git add handlers.go handlers_test.go
git commit -m "feat: opener agent with Host check and hub resolve endpoint

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Links management API

**Files:**
- Modify: `handlers.go` (extend `Handler()`, add three handlers)
- Test: `handlers_test.go` (append tests)

**Interfaces:**
- Consumes: `Server`, `writeJSON` (Task 4); `Store.All/Set/Delete` (Task 2); `newTestServer`, `get` (Task 3)
- Produces:
  - `GET /api/links` → `200` JSON object of slug→target
  - `POST /api/links` with `{"slug":…,"target":…}` → `200 {"slug":…,"target":…}`; `400` JSON `{"error":…}` on invalid input
  - `DELETE /api/links/{slug}` → `200 {"deleted":…}`; `404` on unknown slug

- [ ] **Step 1: Write the failing tests**

Append to `handlers_test.go`:

```go
func TestAPILinksList(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/api/links", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var links map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &links); err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links["daily"] != "obsidian://daily?vault=Main" {
		t.Errorf("links = %v", links)
	}
}

func TestAPILinksAdd(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	body := strings.NewReader(`{"slug":"proj","target":"~/Code/my-app"}`)
	req := httptest.NewRequest("POST", "http://hub.ts.example/api/links", body)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	target, ok, _ := srv.store.Get("proj")
	if !ok || target != "~/Code/my-app" {
		t.Errorf("Get(proj) = %q, %v", target, ok)
	}
}

func TestAPILinksAddInvalid(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	for _, payload := range []string{
		`{"slug":"Bad Slug","target":"/x"}`,
		`{"slug":"api","target":"/x"}`,
		`{"slug":"ok","target":""}`,
		`not json`,
	} {
		req := httptest.NewRequest("POST", "http://hub.ts.example/api/links", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload %q: status = %d, want 400", payload, rec.Code)
		}
	}
}

func TestAPILinksDelete(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	req := httptest.NewRequest("DELETE", "http://hub.ts.example/api/links/budget", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, ok, _ := srv.store.Get("budget"); ok {
		t.Error("budget still present after delete")
	}
	req = httptest.NewRequest("DELETE", "http://hub.ts.example/api/links/nope", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./...`
Expected: FAIL — API routes return 404 / method not allowed

- [ ] **Step 3: Write the implementation**

In `handlers.go`, add three lines inside the `if s.store != nil` block of `Handler()`:

```go
	if s.store != nil { // hub role
		mux.HandleFunc("GET /{slug}", s.handleShortlink)
		mux.HandleFunc("GET /api/resolve/{slug}", s.handleResolve)
		mux.HandleFunc("GET /api/links", s.handleListLinks)
		mux.HandleFunc("POST /api/links", s.handleSetLink)
		mux.HandleFunc("DELETE /api/links/{slug}", s.handleDeleteLink)
	}
```

Append to `handlers.go`:

```go
func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.All()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, links)
}

func (s *Server) handleSetLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug   string `json:"slug"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if err := s.store.Set(body.Slug, body.Target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": body.Slug, "target": body.Target})
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if err := s.store.Delete(slug); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": slug})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: `ok  github.com/loschenbd/shortlinker`

- [ ] **Step 5: Commit**

```bash
git add handlers.go handlers_test.go
git commit -m "feat: JSON API for listing, adding, and deleting links

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: main() wiring + smoke test

**Files:**
- Create: `main.go`

**Interfaces:**
- Consumes: `NewServer` (Task 3), `Store` (Task 2)
- Produces: the `shortlinker` binary. Flags: `--port` (default `7777`), `--links` (default `~/.config/shortlinker/links.txt`), `--hub` (default empty; non-empty switches to agent-only role)

- [ ] **Step 1: Write main.go**

Create `main.go`:

```go
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	var (
		port  = flag.String("port", "7777", "port to listen on (loopback only)")
		links = flag.String("links", filepath.Join(home, ".config", "shortlinker", "links.txt"), "path to links file (hub role)")
		hub   = flag.String("hub", "", "hub base URL, e.g. https://mac.tailnet.ts.net; setting this runs an opener agent only")
	)
	flag.Parse()

	var store *Store
	if *hub == "" {
		store = &Store{Path: *links}
	}
	srv := NewServer(store, *hub, *port, home)

	addr := "127.0.0.1:" + *port
	if store != nil {
		log.Printf("shortlinker hub on http://%s (links: %s)", addr, *links)
	} else {
		log.Printf("shortlinker agent on http://%s (hub: %s)", addr, *hub)
	}
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
```

- [ ] **Step 2: Build and run the full test suite**

Run: `go vet ./... && go test ./... && go build -o bin/shortlinker .`
Expected: vet clean, tests `ok`, binary at `bin/shortlinker`

- [ ] **Step 3: Manual smoke test**

```bash
printf 'demo = https://example.com\nhere = %s\n' "$PWD" > /tmp/shortlinker-smoke.txt
./bin/shortlinker --links /tmp/shortlinker-smoke.txt --port 7788 &
SL_PID=$!
sleep 1
curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' http://127.0.0.1:7788/demo
# Expected: 302 https://example.com/
curl -s -H 'Host: localhost:7788' http://127.0.0.1:7788/o/here | grep -c Opened
# Expected: 1  (and a Finder window for the repo directory opens)
curl -s -X POST http://127.0.0.1:7788/api/links -d '{"slug":"added","target":"https://go.dev"}'
# Expected: {"slug":"added","target":"https://go.dev"}
curl -s http://127.0.0.1:7788/api/links
# Expected: JSON containing demo, here, added
kill $SL_PID
```

Expected: outputs as annotated above. If any differ, stop and debug before committing.

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat: main entrypoint with role flags, loopback-only listener

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: Install script, Makefile, README

**Files:**
- Create: `install.sh`, `Makefile`, `README.md`

**Interfaces:**
- Consumes: the built binary (Task 6)
- Produces: `make build` / `make test` / `make install`; `HUB=<url> make install` for agent-role Macs; LaunchAgent labeled `com.<username>.shortlinker`

- [ ] **Step 1: Write install.sh**

Create `install.sh`:

```sh
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
  echo "One-time tailnet exposure: tailscale serve --bg 7777"
fi
```

Then: `chmod +x install.sh`

- [ ] **Step 2: Write the Makefile**

Create `Makefile`:

```makefile
.PHONY: build test install

build:
	go build -o bin/shortlinker .

test:
	go vet ./...
	go test ./...

install:
	./install.sh
```

- [ ] **Step 3: Write the README**

Create `README.md`:

```markdown
# shortlinker

Self-hosted, tailnet-only short links. Paste `https://<hub>.<tailnet>.ts.net/budget`
into Notion; clicking it opens app deep links (`obsidian://…`) on Mac and iOS,
and opens files/folders on whichever Mac you clicked. One Go binary, zero
dependencies, nothing leaves your Tailscale network.

Design: `docs/superpowers/specs/2026-07-06-shortlinker-design.md`

## Install

**Hub (the designated, usually-awake Mac):**

    make install
    tailscale serve --bg 7777        # one-time; publishes https://<hub>.<tailnet>.ts.net

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
```

- [ ] **Step 4: Verify**

Run: `sh -n install.sh && make test && make build`
Expected: no syntax errors, tests pass, binary builds

- [ ] **Step 5: Commit**

```bash
git add install.sh Makefile README.md
git commit -m "docs: install script, Makefile, and README

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Post-plan verification (manual, per device)

Not part of the automated tasks — run after deployment:

1. Hub Mac: `make install`, `tailscale serve --bg 7777`, add a test link, click `https://<hub>.<tailnet>.ts.net/<slug>` in Notion → file opens.
2. Second Mac: `HUB=… make install`, click the same Notion link → file opens *on the second Mac*.
3. iPhone: tap a deep-link slug → app opens; tap a file slug → "Mac-only link" page.
