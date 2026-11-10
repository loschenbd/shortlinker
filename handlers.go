package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
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
//     this Mac) and the link-changing API.
//
// Every request through `tailscale serve` arrives from 127.0.0.1 with
// whatever Host header the sender chose, so no check on the public port can
// tell this Mac from another tailnet device. A port serve doesn't forward
// can't be reached from the tailnet at all. An opener agent (another Mac)
// runs only the local port.
type Server struct {
	store     *Store                    // nil on agent-only instances
	hubURL    string                    // set on agent-only instances
	localPort string                    // where /o/ listens; hubs redirect Macs here
	home      string                    // for ~ expansion
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
	return mux
}

// LocalHandler serves the loopback-only port: opening files on this Mac and,
// on a hub, changing links.
func (s *Server) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /o/{slug}", s.handleOpen)
	if s.store != nil {
		mux.HandleFunc("GET /api/links", s.handleListLinks)
		mux.HandleFunc("POST /api/links", s.handleSetLink)
		mux.HandleFunc("DELETE /api/links/{slug}", s.handleDeleteLink)
		mux.HandleFunc("POST /api/prune", s.handlePrune)
	}
	return loopbackOnly(mux)
}

// loopbackOnly refuses requests whose Host isn't localhost or 127.0.0.1, so a
// web page can't reach the local port through a DNS-rebinding name.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if host != "localhost" && host != "127.0.0.1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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
	// File/folder target: mobile can't open it, so the hub serves its
	// contents; Macs bounce to their own local opener.
	if isMobile(r.UserAgent()) {
		http.Redirect(w, r, "/f/"+slug, http.StatusFound)
		return
	}
	// NOTE: every Mac must run its opener on the same local port (default 7778).
	http.Redirect(w, r, "http://localhost:"+s.localPort+"/o/"+slug, http.StatusFound)
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
	act, page := s.open, openedPage
	if runsCode(full) {
		act, page = s.reveal, revealedPage
	}
	if err := act(full); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, page)
}

// codeExts are kinds of file that `open` runs or installs rather than shows.
var codeExts = map[string]bool{
	".app": true, ".command": true, ".tool": true, ".terminal": true,
	".workflow": true, ".action": true, ".shortcut": true,
	".scpt": true, ".scptd": true, ".applescript": true,
	".pkg": true, ".mpkg": true, ".jar": true, ".prefpane": true,
	".sh": true, ".bash": true, ".zsh": true, ".csh": true,
	".py": true, ".rb": true, ".pl": true,
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

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.All()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, links)
}

// refuseWebWrite rejects link changes sent by a web page or without a JSON
// Content-Type (which a page can't send cross-origin without a preflight).
func refuseWebWrite(w http.ResponseWriter, r *http.Request) bool {
	if fromWebPage(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "links can't be changed from a web page"})
		return true
	}
	if r.Method == http.MethodDelete {
		return false
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return true
	}
	return false
}

func (s *Server) handleSetLink(w http.ResponseWriter, r *http.Request) {
	if refuseWebWrite(w, r) {
		return
	}
	var body struct {
		Slug   string `json:"slug"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if body.Slug == "" { // a file or folder with no slug yet: pick one
		slug, created, err := s.store.Ensure(body.Target, s.home)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "target": body.Target, "created": created})
		return
	}
	if err := s.store.Set(body.Slug, body.Target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": body.Slug, "target": body.Target})
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	if refuseWebWrite(w, r) {
		return
	}
	slug := r.PathValue("slug")
	if err := s.store.Delete(slug); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": slug})
}

// handlePrune removes sl-link links whose file is gone. Body: {"dry_run": bool}.
func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	if refuseWebWrite(w, r) {
		return
	}
	var body struct {
		DryRun bool `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	removed, err := s.store.PruneAuto(s.home, body.DryRun)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "dry_run": body.DryRun})
}
