package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// autoMarker starts the section of links.txt that sl-link writes. Links
// above it are hand-made; `shortlinker prune` only ever removes links below it.
const autoMarker = "# Added by sl-link. `shortlinker prune` removes the ones whose file is gone."

func isAutoMarker(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "# Added by sl-link")
}

// Store is a file-backed link store. Every read re-reads the file so edits
// made by hand or by other processes take effect immediately.
type Store struct {
	Path string
	// mu serializes writes. Each write rewrites the whole file, so two at
	// once (an agent linking several files in parallel) would drop a line.
	mu sync.Mutex
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
	l, ok, err := s.Lookup(slug)
	return l.Target, ok, err
}

// Lookup returns a link with its phone page and whether sl-link made it.
func (s *Store) Lookup(slug string) (Link, bool, error) {
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, err
	}
	links, _ := parseEntries(string(data))
	l, ok := links[slug]
	return l, ok, nil
}

// Set adds or updates a hand-made link, preserving comments, blank lines,
// and the order of existing entries. New slugs go at the end of the
// hand-made links, above the sl-link section.
func (s *Store) Set(slug, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set(slug, target, false)
}

func (s *Store) set(slug, target string, auto bool) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid slug %q (must match [a-z0-9-]+ and not be reserved)", slug)
	}
	if target == "" {
		return fmt.Errorf("empty target")
	}
	lines, err := s.readLines()
	if err != nil {
		return err
	}
	newLine := formatLine(slug, target, "")
	for i, line := range lines {
		if lineSlug(line) == slug {
			// Keep the phone page if the link still points at the same file.
			_, raw, _ := strings.Cut(line, "=")
			if old, phone := splitTarget(strings.TrimSpace(raw)); old == target {
				newLine = formatLine(slug, target, phone)
			}
			lines[i] = newLine
			return s.writeLines(lines)
		}
	}
	m := markerIndex(lines)
	switch {
	case auto && m < 0:
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, autoMarker, newLine)
	case auto || m < 0:
		lines = append(lines, newLine)
	default: // hand-made, and an sl-link section exists: insert above it
		at := m
		for at > 0 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		lines = append(lines[:at], append([]string{newLine}, lines[at:]...)...)
	}
	return s.writeLines(lines)
}

// Ensure returns the slug of a file or folder target, registering one named
// after the file when it has none. Targets match after ~ expansion and
// cleaning, so "~/a.md" and "/Users/me/a.md" share a slug, and linking the
// same file again returns the same link. New links go in the sl-link section.
func (s *Store) Ensure(target, home string) (slug string, created bool, err error) {
	if target == "" {
		return "", false, fmt.Errorf("empty target")
	}
	if IsRedirect(target) {
		return "", false, fmt.Errorf("a URL target needs a slug; only files and folders get one picked for them")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	links, err := s.All()
	if err != nil {
		return "", false, err
	}
	want := filepath.Clean(ExpandPath(target, home))
	var matches []string
	for sl, t := range links {
		if !IsRedirect(t) && filepath.Clean(ExpandPath(t, home)) == want {
			matches = append(matches, sl)
		}
	}
	if len(matches) > 0 {
		sort.Strings(matches)
		return matches[0], false, nil
	}
	free := func(c string) bool {
		_, taken := links[c]
		return !taken && ValidSlug(c)
	}
	// The file's name, then its folder plus its name (a second README.md
	// becomes my-app-readme-md), then numbered.
	base := SlugFromPath(want)
	slug = base
	if !free(slug) {
		if parent := SlugFromPath(filepath.Dir(want)); parent != "file" {
			slug = parent + "-" + base
		}
	}
	for n := 2; !free(slug); n++ {
		slug = fmt.Sprintf("%s-%d", base, n)
	}
	if err := s.set(slug, target, true); err != nil {
		return "", false, err
	}
	return slug, true, nil
}

// SetPhone sets the page phones get for a file link instead of the file
// (its SilverBullet page). An empty url removes it.
func (s *Store) SetPhone(slug, url string) error {
	if url != "" && !isWebURL(url) {
		return fmt.Errorf("phone page must be an http(s) URL, got %q", url)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lines, err := s.readLines()
	if err != nil {
		return err
	}
	for i, line := range lines {
		if lineSlug(line) != slug {
			continue
		}
		_, raw, _ := strings.Cut(line, "=")
		target, _ := splitTarget(strings.TrimSpace(raw))
		if IsRedirect(target) {
			return fmt.Errorf("%q redirects to a URL, so it has no phone page", slug)
		}
		lines[i] = formatLine(slug, target, url)
		return s.writeLines(lines)
	}
	return fmt.Errorf("unknown slug %q", slug)
}

// PruneAuto removes sl-link links whose file or folder no longer exists and
// returns their slugs. Hand-made links are never touched. With dryRun it
// only reports what it would remove.
func (s *Store) PruneAuto(home string, dryRun bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines, err := s.readLines()
	if err != nil {
		return nil, err
	}
	removed := []string{}
	m := markerIndex(lines)
	if m < 0 {
		return removed, nil
	}
	kept := append([]string{}, lines[:m+1]...)
	for _, line := range lines[m+1:] {
		if slug := lineSlug(line); slug != "" {
			_, raw, _ := strings.Cut(line, "=")
			target, _ := splitTarget(strings.TrimSpace(raw))
			if target != "" && !IsRedirect(target) {
				if _, err := os.Stat(ExpandPath(target, home)); os.IsNotExist(err) {
					removed = append(removed, slug)
					continue
				}
			}
		}
		kept = append(kept, line)
	}
	if dryRun || len(removed) == 0 {
		return removed, nil
	}
	return removed, s.writeLines(kept)
}

func (s *Store) Delete(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines, err := s.readLines()
	if err != nil {
		return err
	}
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

// markerIndex is the line index of autoMarker, or -1.
func markerIndex(lines []string) int {
	for i, line := range lines {
		if isAutoMarker(line) {
			return i
		}
	}
	return -1
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

func (s *Store) readLines() ([]string, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

// writeLines replaces the file atomically: it writes a temporary file next
// to it and renames it into place, so a click that reads the file mid-save,
// or a crash, never sees half a file. A symlinked links.txt is followed, so
// the symlink survives and the real file is the one replaced.
func (s *Store) writeLines(lines []string) error {
	path := s.Path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".links-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if _, err := tmp.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
