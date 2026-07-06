package main

import (
	"encoding/json"
	"fmt"
	"net"
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
	mux.HandleFunc("GET /o/{slug}", s.handleOpen)
	if s.store != nil { // hub role
		mux.HandleFunc("GET /{slug}", s.handleShortlink)
		mux.HandleFunc("GET /api/resolve/{slug}", s.handleResolve)
		mux.HandleFunc("GET /api/links", s.handleListLinks)
		mux.HandleFunc("POST /api/links", s.handleSetLink)
		mux.HandleFunc("DELETE /api/links/{slug}", s.handleDeleteLink)
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
