package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	devices   *devices                  // tailnet device names and this Mac's own; nil until known

	refusedMu sync.Mutex
	refused   map[string]bool // Hosts the public port refused, logged once each
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
	return s.publicHostOnly(mux)
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
		mux.HandleFunc("POST /api/links/{slug}/archive", s.requireAuth(s.handleArchive))
		mux.HandleFunc("POST /api/links/{slug}/note", s.requireAuth(s.handleSetNote))
		mux.HandleFunc("POST /api/links/{slug}/keep", s.requireAuth(s.handleKeep))
		mux.HandleFunc("GET /api/hub-proof", s.handleHubProof) // before any token is sent
		mux.HandleFunc("GET /api/links/{slug}/clicks", s.requireAuth(s.handleClicks))
		mux.HandleFunc("GET /api/stats", s.requireAuth(s.handleStats))
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
func (s *Server) publicHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowedPublicHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedPublicHost accepts loopback and this hub's own tailnet name: the
// one in its saved address, or the one Tailscale reports. Until either is
// known it accepts any *.ts.net name.
func (s *Server) allowedPublicHost(hostport string) bool {
	host := strings.ToLower(hostOf(hostport))
	if host == "localhost" || host == "127.0.0.1" {
		return true
	}
	known := append([]string(nil), s.devices.selfNames()...)
	if u, err := url.Parse(s.publicURL()); err == nil && u.Hostname() != "" {
		known = append(known, strings.ToLower(u.Hostname()))
	}
	if len(known) == 0 {
		return strings.HasSuffix(host, ".ts.net")
	}
	for _, k := range known {
		if host == k {
			return true
		}
	}
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()
	if s.refused == nil {
		s.refused = map[string]bool{}
	}
	if !s.refused[host] && len(s.refused) < 50 {
		s.refused[host] = true
		log.Printf("refused a request for host %q: this hub answers to %s. If its tailnet name changed, delete ~/.config/shortlinker/url and restart it.", host, strings.Join(known, ", "))
	}
	return false
}

func isMobile(userAgent string) bool {
	return strings.Contains(userAgent, "iPhone") ||
		strings.Contains(userAgent, "iPad") ||
		strings.Contains(userAgent, "Android")
}

// handleShortlink serves the canonical short URLs (hub role) and logs each
// click with its route: redirect (a URL target), open (a Mac opens it),
// finder (a Mac shows it in Finder), phone-page, serve (a phone gets the
// file), missing (the file is gone), renamed (sent on to the new slug),
// removed (a tombstone) or unknown.
func (s *Server) handleShortlink(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	e, ok, err := s.store.Find(slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	click := Click{Slug: slug, Device: clickDevice(r), Client: clientKind(r.UserAgent()), Ref: refOrigin(r)}
	route := func(name string) {
		if click.Route == "" {
			click.Route = name
		}
	}
	defer func() {
		if click.Route == "" {
			return
		}
		if err := s.store.RecordClick(click); err != nil {
			log.Printf("recording a click on %s: %v", slug, err)
		}
	}()
	if !ok {
		route("unknown")
		if !ValidSlug(slug) || len(slug) > maxLoggedSlug {
			click.Route = "" // not a slug anyone could have been given: not worth a line
		}
		http.NotFound(w, r)
		return
	}
	if gone := e.Rec; gone != nil && gone.Removed != nil {
		if gone.Removed.Why == "renamed" && ValidSlug(gone.Removed.To) {
			route("renamed")
			dest := "/" + gone.Removed.To
			if r.URL.RawQuery != "" {
				dest += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, dest, http.StatusFound)
			return
		}
		route("removed")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		fmt.Fprintf(w, gonePage, html.EscapeString(e.Target))
		return
	}
	link := e.Link
	if IsRedirect(link.Target) {
		route("redirect")
		http.Redirect(w, r, link.Target, http.StatusFound)
		return
	}
	if _, err := os.Stat(ExpandPath(link.Target, s.home)); err != nil {
		route("missing")
	}
	// File/folder target: a phone can't open it, so it gets the link's phone
	// page (a Markdown file's SilverBullet page) or the file itself; Macs
	// bounce to their own local opener.
	if isMobile(r.UserAgent()) {
		if link.Phone != "" {
			route("phone-page")
			http.Redirect(w, r, link.Phone, http.StatusFound)
			return
		}
		route("serve")
		http.Redirect(w, r, "/f/"+slug, http.StatusFound)
		return
	}
	// NOTE: every Mac must run its opener on the same local port (default 7778).
	dest := "http://localhost:" + s.localPort + "/o/" + slug
	if r.URL.Query().Has("finder") { // ?finder: show it in Finder instead
		dest += "?reveal=1"
		route("finder")
	}
	route("open")
	http.Redirect(w, r, dest, http.StatusFound)
}

// maxLoggedSlug is the longest unknown slug the click log keeps; picked
// slugs are at most 60 characters plus a number.
const maxLoggedSlug = 100

// clickDevice is the clicking device's tailnet address, which `tailscale
// serve` puts in X-Forwarded-For, or "local" for a request made on this Mac.
// Anything on this Mac can set the header, so it's for statistics only.
func clickDevice(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
			return ip.String()
		}
	}
	return "local"
}

func clientKind(userAgent string) string {
	switch {
	case isMobile(userAgent):
		return "phone"
	case strings.Contains(userAgent, "Macintosh"):
		return "mac"
	}
	return "other"
}

// refOrigin is the referring site's origin (https://www.notion.so), never
// its full URL, or "" when there's none or it's this hub.
func refOrigin(r *http.Request) string {
	u, err := url.Parse(r.Referer())
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(u.Host) > 255 {
		return ""
	}
	if strings.EqualFold(u.Hostname(), hostOf(r.Host)) {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}

// handleOpen runs `open` on a registered slug's target (local port only).
// Apps and scripts are shown in Finder instead: any web page can send the
// browser here, and opening one of those would run it.
func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	// A click arrives as a navigation. A page that fetches /o/ in the
	// background, with an <img> or fetch(), could open your files without
	// your seeing it; browsers say which this is in Sec-Fetch-Dest.
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" && dest != "document" {
		http.Error(w, "links open when you click them, not from inside a page", http.StatusForbidden)
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
// an installer, or any executable file. A symlink is judged by its own name
// and by what it points to, since `open` follows it: guide.pdf can point
// at an app.
func runsCode(path string) bool {
	names := []string{path}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		names = append(names, real)
	}
	for _, p := range names {
		if codeExts[strings.ToLower(filepath.Ext(strings.TrimRight(p, "/")))] {
			return true
		}
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
	Slug        string     `json:"slug"`
	Target      string     `json:"target"`
	Phone       string     `json:"phone,omitempty"`
	Kind        string     `json:"kind"` // "url", "file", "folder" or "missing"
	Kept        bool       `json:"kept"`
	Created     *time.Time `json:"created,omitempty"`
	Origin      string     `json:"origin,omitempty"`
	Project     string     `json:"project,omitempty"`
	Session     string     `json:"session,omitempty"`
	Note        string     `json:"note,omitempty"`
	Linked      int        `json:"linked,omitempty"`
	LastLinked  *time.Time `json:"last_linked,omitempty"`
	Clicks      int        `json:"clicks"`
	LastClicked *time.Time `json:"last_clicked,omitempty"`
	LastDevice  string     `json:"last_device,omitempty"` // named, as in clickView
	Archived    *time.Time `json:"archived,omitempty"`
	ArchivesAt  *time.Time `json:"archives_at,omitempty"` // when it archives if nothing happens first
	Removed     *Removal   `json:"removed,omitempty"`
}

func (s *Server) details(entries []Entry) []linkDetails {
	out := make([]linkDetails, 0, len(entries))
	for _, e := range entries {
		r := e.Rec
		d := linkDetails{Slug: e.Slug, Target: e.Target, Phone: e.Phone, Kind: "url", Kept: r.Kept,
			Created: timePtr(r.Created), Origin: r.Origin, Project: r.Project, Session: r.Session, Note: r.Note,
			Linked: r.Linked, LastLinked: r.LastLinked, Clicks: r.Clicks, LastClicked: r.LastClicked,
			Archived: r.Archived, Removed: r.Removed}
		if r.LastDevice != "" {
			d.LastDevice = s.viewClick(Click{Device: r.LastDevice}).Device
		}
		if r.archivable() {
			d.ArchivesAt = timePtr(r.ArchivesAt())
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

// handleListLinks lists the links in use as {slug: target}, or with
// ?details=1 as {"hub": <address>, "links": [linkDetails...]}, kept first.
// With details, ?archived=1 adds archived links and ?removed=1 tombstones.
func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	details := q.Get("details") == "1"
	entries, err := s.store.Listing(details && q.Get("archived") == "1", details && q.Get("removed") == "1")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if details {
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
		Slug    string `json:"slug"`
		Target  string `json:"target"`
		Phone   string `json:"phone"`
		Origin  string `json:"origin"`  // sl-link, finder, api, manage or import
		Session string `json:"session"` // the agent session making it
		Note    string `json:"note"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	meta := Meta{Origin: body.Origin, Session: body.Session, Note: body.Note}
	if body.Slug == "" { // a file or folder with no slug yet: pick one
		slug, created, err := s.store.Ensure(body.Target, s.home, meta)
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
	if err := s.store.Set(body.Slug, body.Target, s.home, meta); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if body.Phone != "" {
		if err := s.store.SetPhone(body.Slug, body.Phone); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the link is saved, but not its phone page: " + err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": body.Slug, "target": body.Target, "kept": true})
}

// handleSetNote replaces a link's note. Body: {"note": "..."}; "" removes it.
func (s *Server) handleSetNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note string `json:"note"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	slug := r.PathValue("slug")
	if err := s.store.SetNote(slug, body.Note); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "note": cleanNote(body.Note)})
}

// handleKeep keeps a link or stops keeping it. Body: {"kept": bool}.
func (s *Server) handleKeep(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kept bool `json:"kept"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	slug := r.PathValue("slug")
	if err := s.store.SetKept(slug, body.Kept); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "kept": body.Kept})
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

// handleArchive archives a link that isn't kept, or restores it. Body: {"archived": bool}.
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Archived bool `json:"archived"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	slug := r.PathValue("slug")
	if err := s.store.SetArchived(slug, body.Archived); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "archived": body.Archived})
}

// clickView is a click as the API shows it: the device named when it's known.
type clickView struct {
	T      time.Time `json:"t"`
	Slug   string    `json:"slug"`
	Device string    `json:"device"`        // its name, else its address, or "local"
	IP     string    `json:"ip,omitempty"`  // its tailnet address
	Client string    `json:"client"`        // mac, phone or other
	Route  string    `json:"route"`         // see handleShortlink
	Ref    string    `json:"ref,omitempty"` // the referring site's origin
}

func (s *Server) viewClick(c Click) clickView {
	v := clickView{T: c.T, Slug: c.Slug, Device: c.Device, Client: c.Client, Route: c.Route, Ref: c.Ref}
	if c.Device != "local" {
		v.IP = c.Device
		if name := s.devices.name(c.Device); name != "" {
			v.Device = name
		}
	}
	return v
}

// handleClicks lists a link's clicks, newest first: ?limit=N (default 200).
func (s *Server) handleClicks(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	clicks, err := s.store.Clicks(r.PathValue("slug"), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]clickView, 0, len(clicks))
	for _, c := range clicks {
		out = append(out, s.viewClick(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleStats sums up the links and the last 7 and 30 days of clicks.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.Listing(true, true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	clicks, err := s.store.Clicks("", 0)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	now := s.store.now()
	week, month := now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour)
	st := struct {
		Links        int            `json:"links"` // in use: not archived or removed
		Kept         int            `json:"kept"`
		Archived     int            `json:"archived"`
		Removed      int            `json:"removed"`
		Missing      int            `json:"missing"` // links in use whose file is gone
		MadeWeek     int            `json:"made_week"`
		NeverClicked int            `json:"never_clicked"` // links in use, not kept
		ClicksWeek   int            `json:"clicks_week"`
		ClicksMonth  int            `json:"clicks_month"`
		ByDevice     map[string]int `json:"by_device"` // last 30 days
		ByRef        map[string]int `json:"by_ref"`    // last 30 days, by domain
	}{ByDevice: map[string]int{}, ByRef: map[string]int{}}
	for _, d := range s.details(entries) {
		switch {
		case d.Removed != nil:
			st.Removed++
			continue
		case d.Archived != nil:
			st.Archived++
			continue
		case d.Kept:
			st.Kept++
		case d.Clicks == 0:
			st.NeverClicked++
		}
		if d.Created != nil && d.Created.After(week) && d.Origin != "import" { // imported isn't made
			st.MadeWeek++
		}
		st.Links++
		if d.Kind == "missing" {
			st.Missing++
		}
	}
	for _, c := range clicks {
		if !countsAsUse(c.Route) || c.T.Before(month) {
			continue
		}
		st.ClicksMonth++
		if c.T.After(week) {
			st.ClicksWeek++
		}
		st.ByDevice[s.viewClick(c).Device]++
		if c.Ref != "" {
			if u, err := url.Parse(c.Ref); err == nil {
				st.ByRef[strings.TrimPrefix(u.Hostname(), "www.")]++
			}
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// handlePrune removes links that aren't kept and whose file is gone. Body: {"dry_run": bool}.
func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	removed, err := s.store.Prune(s.home, body.DryRun)
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
	writeJSON(w, http.StatusOK, map[string]string{"code": s.auth.newCode()})
}

// handleHubProof answers a command's check that this is the hub before it
// sends the token: ?n=<nonce> gets {"proof": hubProof(token, nonce)}.
// Something else on this port, such as another account's server started
// while the hub was down, can't answer it.
func (s *Server) handleHubProof(w http.ResponseWriter, r *http.Request) {
	n := r.URL.Query().Get("n")
	if !isNonce(n) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "n must be 32 to 64 hex digits"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"proof": hubProof(s.auth.token, n)})
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
	w.Write(managePage)
}
