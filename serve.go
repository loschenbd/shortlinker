package main

import (
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// allowedServeHost reports whether a /f/ request arrived under a name this
// hub answers to: loopback, or its tailnet name via `tailscale serve`. Any
// other Host is a DNS-rebinding page trying to read files through a browser
// on this Mac.
func allowedServeHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return host == "localhost" || host == "127.0.0.1" || strings.HasSuffix(host, ".ts.net")
}

// fromWebPage reports whether a request was sent by a web page (fetch or a
// form) rather than curl, an agent, or a URL typed into the address bar.
// Files served under /f/ share the API's origin, so pages must not be able
// to change links.
func fromWebPage(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site != "" && site != "none"
}

// handleServe serves a file/folder slug's contents over the tailnet (hub
// role), so a phone can read what a Mac would open. A folder slug serves
// the files beneath it and never above: os.Root refuses .. and symlinks that
// leave the folder, and dot-paths (.git, .env) are never served or listed.
func (s *Server) handleServe(w http.ResponseWriter, r *http.Request) {
	if !allowedServeHost(r.Host) {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	slug := r.PathValue("slug")
	target, auto, ok, err := s.store.Entry(slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok || IsRedirect(target) {
		http.NotFound(w, r)
		return
	}
	full := ExpandPath(target, s.home)
	info, err := os.Stat(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache") // edits show on reload
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A page that can run script shares this origin with every other served
	// file, so it could read them all. Only a file someone linked by hand is
	// trusted with that; sl-link's links and anything inside a folder link
	// (a saved web page, say) run sandboxed in an origin of their own.
	if auto || info.IsDir() {
		w = &sandboxWriter{ResponseWriter: w}
	}
	base := "/f/" + slug

	if !info.IsDir() {
		if r.URL.Path != base {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		serveContent(w, r, info.Name(), info.ModTime(), f)
		return
	}

	if r.URL.Path == base {
		http.Redirect(w, r, base+"/", http.StatusFound)
		return
	}
	rest := r.PathValue("path")
	for _, seg := range strings.Split(rest, "/") {
		if strings.HasPrefix(seg, ".") {
			http.NotFound(w, r)
			return
		}
	}
	root, err := os.OpenRoot(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	name := strings.TrimSuffix(rest, "/")
	if name == "" {
		name = "."
	}
	f, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !st.IsDir() {
		serveContent(w, r, st.Name(), st.ModTime(), f)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusFound)
		return
	}
	if idx, err := root.Open(path.Join(name, "index.html")); err == nil {
		defer idx.Close()
		if ist, err := idx.Stat(); err == nil && !ist.IsDir() {
			serveContent(w, r, "index.html", ist.ModTime(), idx)
			return
		}
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeListing(w, slug+"/"+rest, entries)
}

// sandboxPolicy gives a served page an opaque origin: its scripts run, but
// it can't read other files on this origin, its cookies or its storage.
const sandboxPolicy = "sandbox allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads"

// sandboxWriter adds sandboxPolicy to responses whose type can run script
// (HTML, SVG, XML). Other types are left alone: Chrome won't show a PDF
// served with a sandbox policy.
type sandboxWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *sandboxWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if activeType(w.Header().Get("Content-Type")) {
			w.Header().Set("Content-Security-Policy", sandboxPolicy)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *sandboxWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func activeType(contentType string) bool {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch mt {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml":
		return true
	}
	return strings.HasSuffix(mt, "+xml")
}

// plainTextExts are text files a browser would download or misread (Go maps
// .ts to "video/mp2t" and .sh to "application/x-sh"). They're served as
// plain text so a phone shows them.
var plainTextExts = map[string]bool{
	".ts": true, ".mts": true, ".cts": true, ".tsx": true,
	".sh": true, ".bash": true, ".zsh": true,
}

func serveContent(w http.ResponseWriter, r *http.Request, name string, modtime time.Time, content io.ReadSeeker) {
	if plainTextExts[strings.ToLower(path.Ext(name))] {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	http.ServeContent(w, r, name, modtime, content)
}

// writeListing renders a folder as links, folders first, dotfiles left out.
func writeListing(w http.ResponseWriter, title string, entries []fs.DirEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	var items strings.Builder
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		href, label := url.PathEscape(name), html.EscapeString(name)
		if e.IsDir() {
			href, label = href+"/", label+"/"
		}
		fmt.Fprintf(&items, "<li><a href=\"%s\">%s</a></li>\n", href, label)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, listingPage, html.EscapeString(title), html.EscapeString(title), items.String())
}
