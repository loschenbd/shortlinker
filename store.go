package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// AutoFile is sl-link's file, next to links.txt.
const AutoFile = "auto-links.txt"

// autoHeader starts sl-link's file.
var autoHeader = []string{
	"# Links made by sl-link. shortlinker rewrites this file; put your own links in links.txt.",
	"# `shortlinker prune` removes the ones whose file or folder is gone.",
}

// Store keeps links in two plain-text files: yours (links.txt, which you
// edit by hand) and sl-link's (auto-links.txt, which only shortlinker
// writes). Which file a link is in decides how it's treated: sl-link's
// links are served sandboxed and can be pruned. Every read re-reads the
// files, so edits by hand take effect immediately.
type Store struct {
	Path     string // your links: links.txt
	AutoPath string // sl-link's links; "" means AutoFile next to Path
	// mu serializes writes. Each write rewrites a whole file, so two at once
	// (an agent linking several files in parallel) would drop a line.
	mu sync.Mutex

	warnMu   sync.Mutex
	lastWarn string // logged once per change, not on every read
}

func (s *Store) autoPath() string {
	if s.AutoPath != "" {
		return s.AutoPath
	}
	return filepath.Join(filepath.Dir(s.Path), AutoFile)
}

// read returns both files' lines; a missing file has none.
func (s *Store) read() (hand, auto []string, err error) {
	if hand, err = readLines(s.Path); err != nil {
		return nil, nil, err
	}
	if auto, err = readLines(s.autoPath()); err != nil {
		return nil, nil, err
	}
	return hand, auto, nil
}

// All returns every link, yours first and then sl-link's, each in file
// order. A slug of yours hides an sl-link link with the same slug.
func (s *Store) All() ([]Entry, error) {
	hand, auto, err := s.read()
	if err != nil {
		return nil, err
	}
	entries, warnings := parseEntries(strings.Join(hand, "\n"))
	for i := range warnings {
		warnings[i] = filepath.Base(s.Path) + ": " + warnings[i]
	}
	mine := make(map[string]bool, len(entries))
	for _, e := range entries {
		mine[e.Slug] = true
	}
	autoEntries, autoWarnings := parseEntries(strings.Join(auto, "\n"))
	for _, w := range autoWarnings {
		warnings = append(warnings, filepath.Base(s.autoPath())+": "+w)
	}
	for _, e := range autoEntries {
		if mine[e.Slug] {
			warnings = append(warnings, fmt.Sprintf("%s: %q is also in %s, which wins", filepath.Base(s.autoPath()), e.Slug, filepath.Base(s.Path)))
			continue
		}
		e.Auto = true
		entries = append(entries, e)
	}
	s.warn(warnings)
	return entries, nil
}

// warn logs warnings when they differ from the last ones logged, so a bad
// line shows once in the log rather than on every click.
func (s *Store) warn(warnings []string) {
	joined := strings.Join(warnings, "\n")
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if joined == s.lastWarn {
		return
	}
	s.lastWarn = joined
	for _, w := range warnings {
		log.Print(w)
	}
}

// Lookup returns a link and whether it exists.
func (s *Store) Lookup(slug string) (Link, bool, error) {
	entries, err := s.All()
	if err != nil {
		return Link{}, false, err
	}
	for _, e := range entries {
		if e.Slug == slug {
			return e.Link, true, nil
		}
	}
	return Link{}, false, nil
}

func (s *Store) Get(slug string) (string, bool, error) {
	l, ok, err := s.Lookup(slug)
	return l.Target, ok, err
}

// Set adds or updates one of your links, keeping comments, blank lines and
// the order of the others. A slug sl-link made moves to links.txt: naming a
// link makes it yours.
func (s *Store) Set(slug, target string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid slug %q (must match [a-z0-9-]+ and not be o, f or api)", slug)
	}
	if target == "" {
		return fmt.Errorf("empty target")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, auto, err := s.read()
	if err != nil {
		return err
	}
	if i := findLine(hand, slug); i >= 0 {
		hand[i] = formatLine(slug, target, keptPhone(hand[i], target))
		return writeLines(s.Path, hand)
	}
	phone := ""
	i := findLine(auto, slug)
	if i >= 0 {
		phone = keptPhone(auto[i], target)
	}
	// Yours first: if the second write fails, both files hold the slug and
	// yours wins, so the link still works.
	if err := writeLines(s.Path, insertLink(hand, formatLine(slug, target, phone))); err != nil {
		return err
	}
	if i >= 0 {
		return writeLines(s.autoPath(), append(auto[:i:i], auto[i+1:]...))
	}
	return nil
}

// insertLink adds a line at the end of your links. If links.txt still has
// the old sl-link section (Migrate hasn't run), the line goes above it, so
// a later Migrate can't take it for one of sl-link's.
func insertLink(lines []string, line string) []string {
	at := len(lines)
	for i, l := range lines {
		if isAutoMarker(l) {
			at = i
			for at > 0 && strings.TrimSpace(lines[at-1]) == "" {
				at--
			}
			break
		}
	}
	return append(lines[:at:at], append([]string{line}, lines[at:]...)...)
}

// keptPhone is a line's phone page if the line still points at target: a
// link pointed at another file loses its page.
func keptPhone(line, target string) string {
	if _, l, _ := parseLine(line); l.Target == target {
		return l.Phone
	}
	return ""
}

// Ensure returns the slug of a file or folder target, registering one named
// after the file when it has none. Targets match after ~ expansion and
// cleaning, so "~/a.md" and "/Users/me/a.md" share a slug, and linking the
// same file again returns the same link. New links go in sl-link's file.
func (s *Store) Ensure(target, home string) (slug string, created bool, err error) {
	if target == "" {
		return "", false, fmt.Errorf("empty target")
	}
	if IsRedirect(target) {
		return "", false, fmt.Errorf("a URL target needs a slug; only files and folders get one picked for them")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, auto, err := s.read()
	if err != nil {
		return "", false, err
	}
	want := filepath.Clean(ExpandPath(target, home))
	taken := map[string]bool{}
	var matches []string
	for _, lines := range [][]string{hand, auto} {
		for _, line := range lines {
			sl, l, kind := parseLine(line)
			if kind != lineLink {
				continue
			}
			taken[sl] = true
			if !IsRedirect(l.Target) && filepath.Clean(ExpandPath(l.Target, home)) == want {
				matches = append(matches, sl)
			}
		}
	}
	if len(matches) > 0 {
		sort.Strings(matches)
		return matches[0], false, nil
	}
	free := func(c string) bool { return !taken[c] && ValidSlug(c) }
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
	if len(auto) == 0 {
		auto = append([]string{}, autoHeader...)
	}
	if err := writeLines(s.autoPath(), append(auto, formatLine(slug, target, ""))); err != nil {
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
	return s.editLine(slug, func(l Link) (string, error) {
		if IsRedirect(l.Target) {
			return "", fmt.Errorf("%q redirects to a URL, so it has no phone page", slug)
		}
		return formatLine(slug, l.Target, url), nil
	})
}

// Rename gives a link a new slug. The old address stops working.
func (s *Store) Rename(from, to string) error {
	if !ValidSlug(to) {
		return fmt.Errorf("invalid slug %q (must match [a-z0-9-]+ and not be o, f or api)", to)
	}
	if from == to {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, auto, err := s.read()
	if err != nil {
		return err
	}
	if findLine(hand, to) >= 0 || findLine(auto, to) >= 0 {
		return fmt.Errorf("%q is already taken", to)
	}
	for _, f := range []struct {
		path  string
		lines []string
	}{{s.Path, hand}, {s.autoPath(), auto}} {
		if i := findLine(f.lines, from); i >= 0 {
			_, l, _ := parseLine(f.lines[i])
			f.lines[i] = formatLine(to, l.Target, l.Phone)
			return writeLines(f.path, f.lines)
		}
	}
	return fmt.Errorf("unknown slug %q", from)
}

// editLine replaces a slug's line, yours first, with what edit returns.
func (s *Store) editLine(slug string, edit func(Link) (string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, auto, err := s.read()
	if err != nil {
		return err
	}
	for _, f := range []struct {
		path  string
		lines []string
	}{{s.Path, hand}, {s.autoPath(), auto}} {
		if i := findLine(f.lines, slug); i >= 0 {
			_, l, _ := parseLine(f.lines[i])
			line, err := edit(l)
			if err != nil {
				return err
			}
			f.lines[i] = line
			return writeLines(f.path, f.lines)
		}
	}
	return fmt.Errorf("unknown slug %q", slug)
}

// PruneAuto removes sl-link's links whose file or folder no longer exists
// and returns their slugs. Your links are never touched. With dryRun it
// only reports what it would remove.
func (s *Store) PruneAuto(home string, dryRun bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	auto, err := readLines(s.autoPath())
	if err != nil {
		return nil, err
	}
	removed := []string{}
	kept := make([]string, 0, len(auto))
	for _, line := range auto {
		if slug, l, kind := parseLine(line); kind == lineLink && !IsRedirect(l.Target) {
			if _, err := os.Stat(ExpandPath(l.Target, home)); errors.Is(err, os.ErrNotExist) {
				removed = append(removed, slug)
				continue
			}
		}
		kept = append(kept, line)
	}
	if dryRun || len(removed) == 0 {
		return removed, nil
	}
	return removed, writeLines(s.autoPath(), kept)
}

// Delete removes a link from whichever file has it.
func (s *Store) Delete(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, auto, err := s.read()
	if err != nil {
		return err
	}
	found := false
	for _, f := range []struct {
		path  string
		lines []string
	}{{s.Path, hand}, {s.autoPath(), auto}} {
		kept := make([]string, 0, len(f.lines))
		for _, line := range f.lines {
			if sl, _, kind := parseLine(line); kind == lineLink && sl == slug {
				continue
			}
			kept = append(kept, line)
		}
		if len(kept) == len(f.lines) {
			continue
		}
		found = true
		if err := writeLines(f.path, kept); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("unknown slug %q", slug)
	}
	return nil
}

// Migrate moves the section sl-link used to keep at the end of links.txt
// (below "# Added by sl-link") into sl-link's own file, and returns how
// many links moved. Slugs and targets stay exactly as they were. The
// original links.txt is kept as links.txt.before-split. It's safe to run
// again, including after a crash halfway: links already moved are skipped.
func (s *Store) Migrate() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, auto, err := s.read()
	if err != nil {
		return 0, err
	}
	m := -1
	for i, line := range hand {
		if isAutoMarker(line) {
			m = i
			break
		}
	}
	if m < 0 {
		return 0, nil
	}
	backup := s.Path + ".before-split"
	if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
		if err := writeLines(backup, hand); err != nil {
			return 0, fmt.Errorf("backing up %s: %w", s.Path, err)
		}
	}
	taken := map[string]bool{}
	for _, line := range auto {
		if slug, _, kind := parseLine(line); kind == lineLink {
			taken[slug] = true
		}
	}
	if len(auto) == 0 {
		auto = append([]string{}, autoHeader...)
	}
	moved := 0
	for _, line := range hand[m+1:] {
		slug, _, kind := parseLine(line)
		switch {
		case strings.TrimSpace(line) == "" || isAutoMarker(line):
			continue
		case kind == lineLink && taken[slug]:
			continue // moved by an earlier run that stopped halfway
		case kind == lineLink:
			taken[slug] = true
			moved++
		}
		auto = append(auto, line) // comments and malformed lines move too, unchanged
	}
	if err := writeLines(s.autoPath(), auto); err != nil {
		return 0, err
	}
	rest := hand[:m]
	for len(rest) > 0 && strings.TrimSpace(rest[len(rest)-1]) == "" {
		rest = rest[:len(rest)-1]
	}
	return moved, writeLines(s.Path, rest)
}

// Tighten makes the link files readable by you only. Before 2026-11 they
// were saved readable by every account on the Mac.
func (s *Store) Tighten() {
	for _, p := range []string{s.Path, s.autoPath()} {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(p, 0o600); err != nil {
				log.Printf("can't make %s private: %v", p, err)
			}
		}
	}
}

// findLine returns the index of slug's first link line, or -1.
func findLine(lines []string, slug string) int {
	for i, line := range lines {
		if sl, _, kind := parseLine(line); kind == lineLink && sl == slug {
			return i
		}
	}
	return -1
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

// writeLines replaces a file atomically: it writes a temporary file next to
// it and renames it into place, so a click that reads the file mid-save, or
// a crash, never sees half a file. A symlink is followed, so the symlink
// survives and the real file is the one replaced. The file is readable by
// you only: it lists private paths.
func writeLines(path string, lines []string) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".links-*.tmp") // created 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	text := ""
	if len(lines) > 0 {
		text = strings.Join(lines, "\n") + "\n"
	}
	if _, err := tmp.WriteString(text); err != nil {
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
	return os.Rename(tmp.Name(), path)
}
