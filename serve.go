package main

import (
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// handleServe serves a file/folder slug's contents over the tailnet (hub
// role), so a phone can read what a Mac would open. A folder slug serves
// the files beneath it and never above: os.Root refuses .. and symlinks that
// leave the folder, and dot-paths (.git, .env) are never served or listed.
// PublicHandler has already checked the Host.
func (s *Server) handleServe(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	link, ok, err := s.store.Lookup(slug)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	target := link.Target
	if !ok || IsRedirect(target) {
		http.NotFound(w, r)
		return
	}
	full := ExpandPath(target, s.home)
	info, err := os.Stat(full)
	if err != nil || s.insideConfig(full) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache") // edits show on reload
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A page that can run script would share this origin with every other
	// served file, and could read them all, so every page runs sandboxed in
	// an origin of its own. Its scripts still run.
	w = &sandboxWriter{ResponseWriter: w}
	base := "/f/" + slug

	if !info.IsDir() {
		if r.URL.Path != base {
			http.NotFound(w, r)
			return
		}
		if !staysNearby(full) {
			log.Printf("not serving %s: it's a symlink to a file outside its folder", slug)
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
	if s.insideConfig(filepath.Join(full, filepath.FromSlash(rest))) { // a link to ~/.config, say
		http.NotFound(w, r)
		return
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

// insideConfig reports whether path, symlinks followed, is shortlinker's own
// folder or inside it. The token, every link's record and the click log live
// there, and /f/ never hands them to the tailnet, whatever is linked: a link
// to the folder, to a backup in it, or to ~/.config above it. Opening them on
// this Mac (/o/) is fine.
func (s *Server) insideConfig(path string) bool {
	cfg := configDir(s.home)
	if real, err := filepath.EvalSymlinks(cfg); err == nil {
		cfg = real
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	rel, err := filepath.Rel(cfg, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// staysNearby reports whether a file link's file, symlinks followed, is in
// the folder it was linked from or below it. A repository can hold
// notes.txt -> ../../.ssh/id_ed25519, and linking notes.txt mustn't serve
// the key. Folder links get the same rule from os.Root.
func staysNearby(full string) bool {
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, real)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sandboxPolicy gives a served page an opaque origin: its scripts run, but
// it can't read other files on this origin, its cookies or its storage.
const sandboxPolicy = "sandbox allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads"

// sandboxWriter adds sandboxPolicy to responses whose type can run script
// (HTML, SVG, XML). Other types (PDFs, images, text) can't run script on
// this origin, so they're left alone.
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
