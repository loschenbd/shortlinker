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
