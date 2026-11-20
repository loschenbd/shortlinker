package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A hub listens on two loopback ports:
//
//   - the public port (default 7777), which `tailscale serve` publishes to
//     the tailnet: short links, /f/ file serving and /api/resolve.
//   - the local port (default 7778), which nothing publishes: /o/ (open on
//     this Mac), the link API (which needs the hub's token) and the manage
//     page.
//
// Every request through `tailscale serve` arrives from 127.0.0.1 with
// whatever Host header the sender chose, so no check on the public port can
// tell this Mac from another tailnet device. A port serve doesn't forward
// can't be reached from the tailnet at all. An opener agent (another Mac)
// runs only the local port, with only /o/.
type Server struct {
	store     *Store                    // nil on agent-only instances
	hubURL    string                    // set on agent-only instances
	localPort string                    // where /o/ listens; hubs redirect Macs here
	home      string                    // for ~ expansion
	auth      *apiAuth                  // the local API's token; nil refuses every API call
	publicURL func() string             // the hub's tailnet address, for the manage page's links
	open      func(target string) error // injectable for tests
	reveal    func(target string) error // shows a target in Finder instead of opening it
	client    *http.Client              // for agent→hub resolve calls
}

func NewServer(store *Store, hubURL, localPort, home string) *Server {
	return &Server{
		store:     store,
		hubURL:    strings.TrimRight(hubURL, "/"),
		localPort: localPort,
		home:      home,
		publicURL: func() string { return savedHubURL(home) },
		open:      func(target string) error { return runOpen(target) },
		reveal:    func(target string) error { return runOpen("-R", target) },
		client:    &http.Client{Timeout: 3 * time.Second},
	}
}

func runOpen(args ...string) error {
	out, err := exec.Command("/usr/bin/open", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("open %q: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PublicHandler serves the port `tailscale serve` publishes (hub role only).
func (s *Server) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{slug}", s.handleShortlink)
	mux.HandleFunc("GET /f/{slug}", s.handleServe)
	mux.HandleFunc("GET /f/{slug}/{path...}", s.handleServe)
	mux.HandleFunc("GET /api/resolve/{slug}", s.handleResolve)
	return publicHostOnly(mux)
}

// LocalHandler serves the loopback-only port: opening files on this Mac and,
// on a hub, the link API and the manage page.
func (s *Server) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /o/{slug}", s.handleOpen)
	if s.store != nil {
		mux.HandleFunc("GET /api/links", s.requireAuth(s.handleListLinks))
		mux.HandleFunc("POST /api/links", s.requireAuth(s.handleSetLink))
		mux.HandleFunc("DELETE /api/links/{slug}", s.requireAuth(s.handleDeleteLink))
		mux.HandleFunc("POST /api/links/{slug}/phone", s.requireAuth(s.handleSetPhone))
		mux.HandleFunc("POST /api/links/{slug}/rename", s.requireAuth(s.handleRename))
		mux.HandleFunc("POST /api/prune", s.requireAuth(s.handlePrune))
		mux.HandleFunc("POST /api/manage-code", s.requireAuth(s.handleManageCode))
		mux.HandleFunc("POST /api/session", s.handleSession) // the code is the credential
		mux.HandleFunc("GET /manage", s.handleManagePage)
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/manage", http.StatusFound)
		})
	}
	return loopbackOnly(mux)
}

func hostOf(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// loopbackOnly refuses requests whose Host isn't localhost or 127.0.0.1, so a
// web page can't reach the local port through a DNS-rebinding name.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host := hostOf(r.Host); host != "localhost" && host != "127.0.0.1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// publicHostOnly refuses requests that didn't arrive under a name this hub
// answers to: loopback, or its tailnet name via `tailscale serve`. Any other
// Host is a DNS-rebinding page in a browser on this Mac trying to read
// files, or which path a slug points to.
func publicHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedPublicHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedPublicHost(hostport string) bool {
	host := hostOf(hostport)
	return host == "localhost" || host == "127.0.0.1" || strings.HasSuffix(host, ".ts.net")
}

func isMobile(userAgent string) bool {
	return strings.Contains(userAgent, "iPhone") ||
		strings.Contains(userAgent, "iPad") ||
		strings.Contains(userAgent, "Android")
}

// handleShortlink serves the canonical short URLs (hub role).
func (s *Server) handleShortlink(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	link, ok, err := s.store.Lookup(slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if IsRedirect(link.Target) {
		http.Redirect(w, r, link.Target, http.StatusFound)
		return
	}
	// File/folder target: a phone can't open it, so it gets the link's phone
	// page (a Markdown file's SilverBullet page) or the file itself; Macs
	// bounce to their own local opener.
	if isMobile(r.UserAgent()) {
		if link.Phone != "" {
			http.Redirect(w, r, link.Phone, http.StatusFound)
			return
		}
		http.Redirect(w, r, "/f/"+slug, http.StatusFound)
		return
	}
	// NOTE: every Mac must run its opener on the same local port (default 7778).
	dest := "http://localhost:" + s.localPort + "/o/" + slug
	if r.URL.Query().Has("finder") { // ?finder: show it in Finder instead
		dest += "?reveal=1"
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// handleOpen runs `open` on a registered slug's target (local port only).
// Apps and scripts are shown in Finder instead: any web page can send the
// browser here, and opening one of those would run it.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
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
	full := ExpandPath(target, s.home)
	if IsRedirect(target) || !filepath.IsAbs(full) {
		// Only paths get here from a hub. Anything else (a relative path, or
		// one starting with "-" that `open` would read as an option) is refused.
		http.Error(w, fmt.Sprintf("%q isn't a full path to a file or folder", target), http.StatusBadRequest)
		return
	}
	act, page := s.open, openedPage
	switch {
	case r.URL.Query().Get("reveal") == "1":
		act, page = s.reveal, shownPage
	case runsCode(full):
		act, page = s.reveal, revealedPage
	}
	if err := act(full); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page)
}

// codeExts are kinds of file that `open` runs, installs or follows to
// something else rather than shows. It can't be complete, which is why
// /o/ only opens targets you registered; this is the second line.
var codeExts = map[string]bool{
	".app": true, ".command": true, ".tool": true, ".terminal": true,
	".workflow": true, ".action": true, ".shortcut": true,
	".scpt": true, ".scptd": true, ".applescript": true,
	".pkg": true, ".mpkg": true, ".jar": true, ".prefpane": true,
	".sh": true, ".bash": true, ".zsh": true, ".csh": true,
	".py": true, ".rb": true, ".pl": true,
	".fileloc":      true, // opens whatever it points to, an app included
	".mobileconfig": true, // installs a configuration profile
	".saver":        true, ".plugin": true, ".bundle": true, ".osax": true, ".kext": true,
}

// runsCode reports whether opening path could run code: an app, a script,
// an installer, or any executable file.
func runsCode(path string) bool {
	if codeExts[strings.ToLower(filepath.Ext(strings.TrimRight(path, "/")))] {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// resolve looks a slug up locally (hub) or via the hub's API (agent).
func (s *Server) resolve(slug string) (string, bool, error) {
	if !ValidSlug(slug) {
		return "", false, nil
	}
	if s.store != nil {
		return s.store.Get(slug)
	}
	resp, err := s.client.Get(s.hubURL + "/api/resolve/" + url.PathEscape(slug))
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
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// linkDetails is one link as the manage page, `shortlinker ls` and the menu
// see it.
type linkDetails struct {
	Slug   string `json:"slug"`
	Target string `json:"target"`
	Phone  string `json:"phone,omitempty"`
	Source string `json:"source"` // "yours" (links.txt) or "sl-link"
	Kind   string `json:"kind"`   // "url", "file", "folder" or "missing"
}

func (s *Server) details(entries []Entry) []linkDetails {
	out := make([]linkDetails, 0, len(entries))
	for _, e := range entries {
		d := linkDetails{Slug: e.Slug, Target: e.Target, Phone: e.Phone, Source: "yours", Kind: "url"}
		if e.Auto {
			d.Source = "sl-link"
		}
		if !IsRedirect(e.Target) {
			switch fi, err := os.Stat(ExpandPath(e.Target, s.home)); {
			case err != nil:
				d.Kind = "missing"
			case fi.IsDir():
				d.Kind = "folder"
			default:
				d.Kind = "file"
			}
		}
		out = append(out, d)
	}
	return out
}

// handleListLinks lists links as {slug: target}, or with ?details=1 as
// {"hub": <address>, "links": [linkDetails...]} in file order.
func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.All()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if r.URL.Query().Get("details") == "1" {
		writeJSON(w, http.StatusOK, map[string]any{"hub": s.publicURL(), "links": s.details(entries)})
		return
	}
	links := make(map[string]string, len(entries))
	for _, e := range entries {
		links[e.Slug] = e.Target
	}
	writeJSON(w, http.StatusOK, links)
}

// requireJSON refuses a body that isn't JSON. A page can't send JSON to
// another origin without a preflight, which this port never answers.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	return true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if !requireJSON(w, r) {
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func (s *Server) handleSetLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug   string `json:"slug"`
		Target string `json:"target"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Slug == "" { // a file or folder with no slug yet: pick one
		slug, created, err := s.store.Ensure(body.Target, s.home)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		link, _, err := s.store.Lookup(slug)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "target": body.Target, "created": created, "phone": link.Phone})
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

// handleSetPhone sets the page phones get for a file link. Body: {"url": "..."}.
func (s *Server) handleSetPhone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	slug := r.PathValue("slug")
	if err := s.store.SetPhone(slug, body.URL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "phone": body.URL})
}

// handleRename gives a link a new slug. Body: {"to": "new-slug"}.
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To string `json:"to"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	slug := r.PathValue("slug")
	if err := s.store.Rename(slug, body.To); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": body.To, "was": slug})
}

// handlePrune removes sl-link links whose file is gone. Body: {"dry_run": bool}.
func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	removed, err := s.store.PruneAuto(s.home, body.DryRun)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "dry_run": body.DryRun})
}

// handleManageCode makes a one-time code that opens the manage page. Only
// the token itself can ask: a session can't mint more sessions.
func (s *Server) handleManageCode(w http.ResponseWriter, r *http.Request) {
	if !s.auth.isToken(bearer(r)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "a code needs the token itself"})
		return
	}
	code := s.auth.newCode()
	writeJSON(w, http.StatusOK, map[string]string{
		"code": code,
		"url":  "http://localhost:" + s.localPort + "/manage#code=" + code,
	})
}

// handleSession trades a one-time code for a session. Body: {"code": "..."}.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if fromOtherSite(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not from another site"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	session, expires, ok := s.auth.redeem(body.Code)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "that code has been used or has expired"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": session, "expires": expires.Unix()})
}

func (s *Server) handleManagePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fmt.Fprint(w, managePage)
}
