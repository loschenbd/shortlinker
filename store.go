package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The files next to links.txt.
const (
	AutoFile       = "auto-links.jsonl" // sl-link's links, one JSON record per line
	legacyAutoFile = "auto-links.txt"   // sl-link's links before they had records
	ClicksFile     = "clicks.jsonl"     // one line per click on a short link
)

// How long an sl-link link stays in the lists without use. Linking the file
// again restarts the clock, and clicking an archived link restores it.
const (
	archiveUnclicked = 30 * 24 * time.Hour  // made, never clicked
	archiveClicked   = 180 * 24 * time.Hour // after the last click
)

// Record is one of sl-link's links with what shortlinker knows about it.
// Removed records are tombstones: they keep the slug, which is never given
// out again, so an old link can't open a different file.
type Record struct {
	Slug        string     `json:"slug"`
	Target      string     `json:"target"`
	Phone       string     `json:"phone,omitempty"`
	Created     time.Time  `json:"created"`
	Origin      string     `json:"origin,omitempty"`  // sl-link, finder, api, manage; "yours" on a tombstone of one of your links
	Project     string     `json:"project,omitempty"` // the git checkout the target is in (the main one, not a worktree)
	Session     string     `json:"session,omitempty"` // the agent session that made it
	Note        string     `json:"note,omitempty"`
	Linked      int        `json:"linked,omitempty"` // times sl-link printed it
	LastLinked  *time.Time `json:"last_linked,omitempty"`
	Clicks      int        `json:"clicks,omitempty"`
	LastClicked *time.Time `json:"last_clicked,omitempty"`
	Archived    *time.Time `json:"archived,omitempty"`
	Removed     *Removal   `json:"removed,omitempty"`
}

// Removal says when and why a link went.
type Removal struct {
	At  time.Time `json:"at"`
	Why string    `json:"why"`          // "pruned", "deleted" or "renamed"
	To  string    `json:"to,omitempty"` // the new slug, for "renamed"
}

func (r *Record) active() bool { return r.Removed == nil && r.Archived == nil }

// ArchivesAt is when the retention pass will archive an active record if
// nothing happens to it before then.
func (r *Record) ArchivesAt() time.Time {
	if r.Clicks == 0 {
		return latest(r.Created, r.LastLinked).Add(archiveUnclicked)
	}
	return latest(r.Created, r.LastLinked, r.LastClicked).Add(archiveClicked)
}

func latest(t time.Time, more ...*time.Time) time.Time {
	for _, m := range more {
		if m != nil && m.After(t) {
			t = *m
		}
	}
	return t
}

func timePtr(t time.Time) *time.Time { return &t }

// Meta is what a caller can say about a new link.
type Meta struct {
	Origin  string // sl-link, finder, api or manage; "" means api
	Session string // the agent session making it, if any
	Note    string // why it was made, if the caller says
}

const (
	maxSession = 128
	maxNote    = 200
)

func (m Meta) clean() (Meta, error) {
	if m.Origin == "" {
		m.Origin = "api"
	}
	switch m.Origin {
	case "sl-link", "finder", "api", "manage":
	default:
		return m, fmt.Errorf("unknown origin %q (sl-link, finder, api or manage)", m.Origin)
	}
	if len(m.Session) > maxSession || strings.IndexFunc(m.Session, func(r rune) bool {
		return !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:-", r)))
	}) >= 0 {
		return m, fmt.Errorf("session must be up to %d letters, digits and ._:-", maxSession)
	}
	m.Note = strings.Join(strings.FieldsFunc(m.Note, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if r := []rune(m.Note); len(r) > maxNote {
		m.Note = strings.TrimSpace(string(r[:maxNote]))
	}
	return m, nil
}

// Click is one use of a short link, as clicks.jsonl keeps it.
type Click struct {
	T      time.Time `json:"t"`
	Slug   string    `json:"slug"`
	Device string    `json:"device,omitempty"` // the clicking device's tailnet address, or "local"
	Client string    `json:"client,omitempty"` // mac, phone or other
	Route  string    `json:"route"`            // what the click did; see handleShortlink
	Ref    string    `json:"ref,omitempty"`    // the referring site's origin, never the full URL
}

// Store keeps your links in links.txt, a plain-text file you edit by hand
// and which every read re-reads, and sl-link's links as records in
// auto-links.jsonl, which only shortlinker writes. Which one a link is in
// decides how it's treated: sl-link's links are served sandboxed, can be
// archived and pruned, and keep a record of their use.
type Store struct {
	Path     string           // your links: links.txt
	AutoPath string           // sl-link's records; "" means AutoFile next to Path
	Now      func() time.Time // for tests; nil means time.Now

	// mu guards everything below and serializes writes: each write rewrites
	// a whole file, so two at once (an agent linking files in parallel)
	// would lose one.
	mu     sync.Mutex
	recs   []*Record
	bad    []string // lines of the records file that didn't parse, written back unchanged
	stamp  fileStamp
	loaded bool

	warnMu   sync.Mutex
	lastWarn string // logged once per change, not on every read
}

type fileStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{fi.ModTime(), fi.Size(), true}
}

func (s *Store) autoPath() string {
	if s.AutoPath != "" {
		return s.AutoPath
	}
	return filepath.Join(filepath.Dir(s.Path), AutoFile)
}

func (s *Store) legacyAutoPath() string { return filepath.Join(filepath.Dir(s.Path), legacyAutoFile) }
func (s *Store) clicksPath() string     { return filepath.Join(filepath.Dir(s.Path), ClicksFile) }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

// load reads the records file if it changed since it was last read or
// written (or was never read), and returns warnings about lines it skipped.
// The caller holds mu.
func (s *Store) load() ([]string, error) {
	st := stampOf(s.autoPath())
	if s.loaded && st == s.stamp {
		return s.loadWarnings(), nil
	}
	lines, err := readLines(s.autoPath())
	if err != nil {
		return nil, err
	}
	recs := make([]*Record, 0, len(lines))
	var bad []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil || !ValidSlug(r.Slug) || r.Target == "" {
			bad = append(bad, line)
			continue
		}
		recs = append(recs, &r)
	}
	s.recs, s.bad, s.stamp, s.loaded = recs, bad, st, true
	return s.loadWarnings(), nil
}

func (s *Store) loadWarnings() []string {
	var w []string
	for _, line := range s.bad {
		if len(line) > 80 {
			line = line[:80] + "…"
		}
		w = append(w, fmt.Sprintf("%s: skipping a line that isn't a link record (kept as is): %s", filepath.Base(s.autoPath()), line))
	}
	seen := map[string]bool{}
	for _, r := range s.recs {
		if seen[r.Slug] {
			w = append(w, fmt.Sprintf("%s: %q has more than one record; the first one counts", filepath.Base(s.autoPath()), r.Slug))
		}
		seen[r.Slug] = true
	}
	return w
}

// save writes the records file. If it fails, the next read starts again
// from what's on disk. The caller holds mu.
func (s *Store) save() error {
	lines := make([]string, 0, len(s.recs)+len(s.bad))
	for _, r := range s.recs {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		lines = append(lines, string(b))
	}
	lines = append(lines, s.bad...)
	if err := writeLines(s.autoPath(), lines); err != nil {
		s.loaded = false
		return err
	}
	s.stamp = stampOf(s.autoPath())
	return nil
}

// rec is slug's record (the first, if there are several), or nil. The caller holds mu.
func (s *Store) rec(slug string) *Record {
	for _, r := range s.recs {
		if r.Slug == slug {
			return r
		}
	}
	return nil
}

func (s *Store) dropRec(slug string) {
	kept := s.recs[:0]
	for _, r := range s.recs {
		if r.Slug != slug {
			kept = append(kept, r)
		}
	}
	s.recs = kept
}

// state reads both files. The caller holds mu.
func (s *Store) state() (hand []string, warnings []string, err error) {
	if hand, err = readLines(s.Path); err != nil {
		return nil, nil, err
	}
	if warnings, err = s.load(); err != nil {
		return nil, nil, err
	}
	return hand, warnings, nil
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

// All returns the links in use: yours first, then sl-link's that aren't
// archived or removed, each in file order.
func (s *Store) All() ([]Entry, error) { return s.Listing(false, false) }

// Listing is All, optionally with archived links and tombstones too. A slug
// of yours hides a record with the same slug.
func (s *Store) Listing(archived, removed bool) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, warnings, err := s.state()
	if err != nil {
		return nil, err
	}
	entries, w := parseEntries(strings.Join(hand, "\n"))
	for _, x := range w {
		warnings = append(warnings, filepath.Base(s.Path)+": "+x)
	}
	mine := make(map[string]bool, len(entries))
	for _, e := range entries {
		mine[e.Slug] = true
	}
	seen := map[string]bool{}
	for _, r := range s.recs {
		if seen[r.Slug] {
			continue
		}
		seen[r.Slug] = true
		if mine[r.Slug] {
			if r.Removed == nil {
				warnings = append(warnings, fmt.Sprintf("%s: %q is also in %s, which wins", filepath.Base(s.autoPath()), r.Slug, filepath.Base(s.Path)))
			}
			continue
		}
		if (r.Archived != nil && !archived) || (r.Removed != nil && !removed) {
			continue
		}
		entries = append(entries, recEntry(r))
	}
	s.warn(warnings)
	return entries, nil
}

func recEntry(r *Record) Entry {
	c := *r
	return Entry{Slug: r.Slug, Link: Link{Target: r.Target, Phone: r.Phone, Auto: true}, Rec: &c}
}

// Find returns slug's link in any state: yours, or sl-link's record,
// archived and removed ones included (look at Rec).
func (s *Store) Find(slug string) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, _, err := s.state()
	if err != nil {
		return Entry{}, false, err
	}
	if i := findLine(hand, slug); i >= 0 {
		_, l, _ := parseLine(hand[i])
		return Entry{Slug: slug, Link: l}, true, nil
	}
	if r := s.rec(slug); r != nil {
		return recEntry(r), true, nil
	}
	return Entry{}, false, nil
}

// Lookup returns a link that works: yours, or sl-link's unless it was
// removed. Archived links still work.
func (s *Store) Lookup(slug string) (Link, bool, error) {
	e, ok, err := s.Find(slug)
	if err != nil || !ok || (e.Rec != nil && e.Rec.Removed != nil) {
		return Link{}, false, err
	}
	return e.Link, true, nil
}

func (s *Store) Get(slug string) (string, bool, error) {
	l, ok, err := s.Lookup(slug)
	return l.Target, ok, err
}

// Set adds or updates one of your links, keeping comments, blank lines and
// the order of the others. Naming a link makes it yours: an sl-link record
// with that slug moves to links.txt, and a tombstone with that slug goes,
// since you chose the slug yourself.
func (s *Store) Set(slug, target string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid slug %q (must match [a-z0-9-]+ and not be o, f or api)", slug)
	}
	if target == "" {
		return fmt.Errorf("empty target")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, _, err := s.state()
	if err != nil {
		return err
	}
	if i := findLine(hand, slug); i >= 0 {
		hand[i] = formatLine(slug, target, keptPhone(hand[i], target))
		return writeLines(s.Path, hand)
	}
	phone := ""
	r := s.rec(slug)
	if r != nil && r.Removed == nil && r.Target == target {
		phone = r.Phone
	}
	// Yours first: if the second write fails, both hold the slug and yours
	// wins, so the link still works.
	if err := writeLines(s.Path, insertLink(hand, formatLine(slug, target, phone))); err != nil {
		return err
	}
	if r != nil {
		s.dropRec(slug)
		return s.save()
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

// Ensure returns the slug of a file or folder target, making an sl-link
// record named after the file when it has none. Targets match after ~
// expansion and cleaning, so "~/a.md" and "/Users/me/a.md" share a slug.
// Linking the same file again returns the same link, counts it, restarts
// its archive clock and restores it if it was archived. A link removed
// because its file was gone comes back if the file is there again. Slugs
// of removed links are never given to another file.
func (s *Store) Ensure(target, home string, meta Meta) (slug string, created bool, err error) {
	if target == "" {
		return "", false, fmt.Errorf("empty target")
	}
	if IsRedirect(target) {
		return "", false, fmt.Errorf("a URL target needs a slug; only files and folders get one picked for them")
	}
	if meta, err = meta.clean(); err != nil {
		return "", false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, _, err := s.state()
	if err != nil {
		return "", false, err
	}
	want := filepath.Clean(ExpandPath(target, home))
	same := func(t string) bool { return !IsRedirect(t) && filepath.Clean(ExpandPath(t, home)) == want }
	taken := map[string]bool{}
	var mine []string
	for _, line := range hand {
		if sl, l, kind := parseLine(line); kind == lineLink {
			taken[sl] = true
			if same(l.Target) {
				mine = append(mine, sl)
			}
		}
	}
	if len(mine) > 0 {
		sort.Strings(mine)
		return mine[0], false, nil
	}
	var live, gone []*Record
	for _, r := range s.recs {
		hidden := taken[r.Slug] // one of yours has this slug, so it isn't this record's to give
		taken[r.Slug] = true
		if hidden || !same(r.Target) || s.rec(r.Slug) != r {
			continue
		}
		switch {
		case r.Removed == nil:
			live = append(live, r)
		case r.Removed.Why != "renamed":
			gone = append(gone, r)
		}
	}
	now := s.now()
	for _, found := range [][]*Record{live, gone} {
		if len(found) == 0 {
			continue
		}
		sort.Slice(found, func(i, j int) bool { return found[i].Slug < found[j].Slug })
		r := found[0]
		r.Removed, r.Archived = nil, nil
		r.Linked++
		r.LastLinked = timePtr(now)
		if meta.Note != "" {
			r.Note = meta.Note
		}
		return r.Slug, false, s.save()
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
	s.recs = append(s.recs, &Record{
		Slug: slug, Target: target, Created: now, Origin: meta.Origin,
		Project: projectOf(want, home), Session: meta.Session, Note: meta.Note,
		Linked: 1, LastLinked: timePtr(now),
	})
	return slug, true, s.save()
}

// SetPhone sets the page phones get for a file link instead of the file
// (its SilverBullet page). An empty url removes it.
func (s *Store) SetPhone(slug, url string) error {
	if url != "" && !isWebURL(url) {
		return fmt.Errorf("phone page must be an http(s) URL, got %q", url)
	}
	noPhone := fmt.Errorf("%q redirects to a URL, so it has no phone page", slug)
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, _, err := s.state()
	if err != nil {
		return err
	}
	if i := findLine(hand, slug); i >= 0 {
		_, l, _ := parseLine(hand[i])
		if IsRedirect(l.Target) {
			return noPhone
		}
		hand[i] = formatLine(slug, l.Target, url)
		return writeLines(s.Path, hand)
	}
	r := s.rec(slug)
	if r == nil || r.Removed != nil {
		return fmt.Errorf("unknown slug %q", slug)
	}
	r.Phone = url
	return s.save()
}

// Rename gives a link a new slug. The old slug keeps a tombstone that sends
// clicks on to the new one, and is never given out again.
func (s *Store) Rename(from, to string) error {
	if !ValidSlug(to) {
		return fmt.Errorf("invalid slug %q (must match [a-z0-9-]+ and not be o, f or api)", to)
	}
	if from == to {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, _, err := s.state()
	if err != nil {
		return err
	}
	if findLine(hand, to) >= 0 {
		return fmt.Errorf("%q is already taken", to)
	}
	if r := s.rec(to); r != nil {
		if r.Removed != nil {
			return fmt.Errorf("%q belonged to a removed link, and slugs aren't reused", to)
		}
		return fmt.Errorf("%q is already taken", to)
	}
	now := s.now()
	moved := &Removal{At: now, Why: "renamed", To: to}
	if i := findLine(hand, from); i >= 0 {
		_, l, _ := parseLine(hand[i])
		hand[i] = formatLine(to, l.Target, l.Phone)
		if err := writeLines(s.Path, hand); err != nil {
			return err
		}
		s.dropRec(from)
		s.recs = append(s.recs, &Record{Slug: from, Target: l.Target, Created: now, Origin: "yours", Removed: moved})
		return s.save()
	}
	r := s.rec(from)
	if r == nil || r.Removed != nil {
		return fmt.Errorf("unknown slug %q", from)
	}
	r.Slug = to
	s.recs = append(s.recs, &Record{Slug: from, Target: r.Target, Created: r.Created, Origin: r.Origin, Removed: moved})
	return s.save()
}

// Delete removes a link: your line goes from links.txt, an sl-link record
// becomes a tombstone. Either way the slug isn't given to another file.
func (s *Store) Delete(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, _, err := s.state()
	if err != nil {
		return err
	}
	now := s.now()
	if i := findLine(hand, slug); i >= 0 {
		_, l, _ := parseLine(hand[i])
		kept := make([]string, 0, len(hand))
		for _, line := range hand {
			if sl, _, kind := parseLine(line); kind == lineLink && sl == slug {
				continue
			}
			kept = append(kept, line)
		}
		if err := writeLines(s.Path, kept); err != nil {
			return err
		}
		s.dropRec(slug)
		s.recs = append(s.recs, &Record{Slug: slug, Target: l.Target, Created: now, Origin: "yours", Removed: &Removal{At: now, Why: "deleted"}})
		return s.save()
	}
	r := s.rec(slug)
	if r == nil || r.Removed != nil {
		return fmt.Errorf("unknown slug %q", slug)
	}
	r.Removed = &Removal{At: now, Why: "deleted"}
	return s.save()
}

// PruneAuto removes sl-link's links whose file or folder no longer exists,
// archived ones included, and returns their slugs. They stay as tombstones.
// Your links are never touched. With dryRun it only reports.
func (s *Store) PruneAuto(home string, dryRun bool) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.load(); err != nil {
		return nil, err
	}
	removed := []string{}
	var dead []*Record
	for _, r := range s.recs {
		if r.Removed != nil || IsRedirect(r.Target) || s.rec(r.Slug) != r {
			continue
		}
		if _, err := os.Stat(ExpandPath(r.Target, home)); errors.Is(err, os.ErrNotExist) {
			removed = append(removed, r.Slug)
			dead = append(dead, r)
		}
	}
	if dryRun || len(dead) == 0 {
		return removed, nil
	}
	now := s.now()
	for _, r := range dead {
		r.Removed = &Removal{At: now, Why: "pruned"}
	}
	return removed, s.save()
}

// SetArchived archives an sl-link link or restores it. Your links don't
// archive.
func (s *Store) SetArchived(slug string, archived bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, _, err := s.state()
	if err != nil {
		return err
	}
	if findLine(hand, slug) >= 0 {
		return fmt.Errorf("%q is one of your links, and those don't archive", slug)
	}
	r := s.rec(slug)
	if r == nil || r.Removed != nil {
		return fmt.Errorf("unknown slug %q", slug)
	}
	switch {
	case archived && r.Archived == nil:
		r.Archived = timePtr(s.now())
	case !archived && r.Archived != nil:
		r.Archived = nil
		r.LastLinked = timePtr(s.now()) // a fresh start, or the next pass archives it again
	default:
		return nil
	}
	return s.save()
}

// Archive is the retention pass: it archives sl-link links nobody has
// clicked within archiveUnclicked of their making (or last linking), and
// clicked ones archiveClicked after their last use. It returns their slugs.
func (s *Store) Archive() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.load(); err != nil {
		return nil, err
	}
	now := s.now()
	archived := []string{}
	for _, r := range s.recs {
		if r.active() && !now.Before(r.ArchivesAt()) {
			r.Archived = timePtr(now)
			archived = append(archived, r.Slug)
		}
	}
	if len(archived) == 0 {
		return archived, nil
	}
	return archived, s.save()
}

// RecordClick appends a click to clicks.jsonl and, for an sl-link link,
// counts it and restores the link if it was archived.
func (s *Store) RecordClick(c Click) error {
	if c.T.IsZero() {
		c.T = s.now()
	}
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := appendLine(s.clicksPath(), line); err != nil {
		return err
	}
	hand, _, err := s.state()
	if err != nil {
		return err
	}
	r := s.rec(c.Slug)
	if r == nil || r.Removed != nil || findLine(hand, c.Slug) >= 0 {
		return nil
	}
	switch c.Route {
	case "unknown", "removed", "renamed":
		return nil
	}
	r.Clicks++
	r.LastClicked = timePtr(c.T)
	r.Archived = nil
	return s.save()
}

// Clicks returns the clicks on slug ("" for all of them), newest first, at
// most limit of them (0 for no limit).
func (s *Store) Clicks(slug string, limit int) ([]Click, error) {
	s.mu.Lock()
	lines, err := readLines(s.clicksPath())
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []Click
	for i := len(lines) - 1; i >= 0; i-- {
		var c Click
		if json.Unmarshal([]byte(lines[i]), &c) != nil || (slug != "" && c.Slug != slug) {
			continue
		}
		out = append(out, c)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Migrate brings older layouts up to date and returns how many links moved
// from each. Slugs, targets and phone pages stay exactly as they were.
//
//   - Before sl-link had a file of its own, its links sat at the end of
//     links.txt below "# Added by sl-link". They move to records, and the
//     old links.txt is kept as links.txt.before-split.
//   - Then sl-link had auto-links.txt, a text file. Its links move to
//     records, and the file is renamed auto-links.txt.before-records.
//
// Moved links are dated now, so their archive clocks start now. It's safe
// to run again, including after a crash halfway: slugs that already have a
// record are skipped.
func (s *Store) Migrate(home string) (fromLinks, fromText int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hand, _, err := s.state()
	if err != nil {
		return 0, 0, err
	}
	now := s.now()
	add := func(lines []string) int {
		n := 0
		for _, line := range lines {
			slug, l, kind := parseLine(line)
			switch {
			case kind == lineBad:
				log.Printf("not moving a malformed line (it's in the backup): %q", line)
				continue
			case kind != lineLink || s.rec(slug) != nil:
				continue
			}
			s.recs = append(s.recs, &Record{
				Slug: slug, Target: l.Target, Phone: l.Phone, Created: now, Origin: "sl-link",
				Project: projectOf(filepath.Clean(ExpandPath(l.Target, home)), home),
			})
			n++
		}
		return n
	}

	m := -1
	for i, line := range hand {
		if isAutoMarker(line) {
			m = i
			break
		}
	}
	if m >= 0 {
		backup := s.Path + ".before-split"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			if err := writeLines(backup, hand); err != nil {
				return 0, 0, fmt.Errorf("backing up %s: %w", s.Path, err)
			}
		}
		fromLinks = add(hand[m+1:])
		if err := s.save(); err != nil {
			return 0, 0, err
		}
		rest := hand[:m]
		for len(rest) > 0 && strings.TrimSpace(rest[len(rest)-1]) == "" {
			rest = rest[:len(rest)-1]
		}
		if err := writeLines(s.Path, rest); err != nil {
			return 0, 0, err
		}
	}

	legacy := s.legacyAutoPath()
	if legacy == s.autoPath() {
		return fromLinks, 0, nil
	}
	text, err := readLines(legacy)
	if err != nil || text == nil {
		return fromLinks, 0, err
	}
	fromText = add(text)
	if err := s.save(); err != nil {
		return fromLinks, 0, err
	}
	return fromLinks, fromText, os.Rename(legacy, legacy+".before-records")
}

// Tighten makes shortlinker's files readable by you only. Before 2026-11
// links.txt was saved readable by every account on the Mac.
func (s *Store) Tighten() {
	for _, p := range []string{s.Path, s.autoPath(), s.clicksPath()} {
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

// projectOf is the git checkout a path is in, written with ~: the main
// checkout when the path is inside a worktree, since worktrees come and go.
// "" if it isn't in one. The path doesn't have to exist any more.
func projectOf(abs, home string) string {
	dir := abs
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		dir = filepath.Dir(abs)
	}
	for {
		if root := repoRoot(dir); root != "" {
			return abbreviateHome(root, home)
		}
		parent := filepath.Dir(dir)
		if parent == dir || dir == home {
			return ""
		}
		dir = parent
	}
}

// repoRoot is dir if it holds a checkout, or for a worktree (whose .git is
// a file pointing into the main checkout's .git/worktrees) the main
// checkout; "" otherwise.
func repoRoot(dir string) string {
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Lstat(gitPath)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return dir
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return dir
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	sep := string(filepath.Separator)
	if i := strings.Index(gitdir, sep+".git"+sep+"worktrees"+sep); i >= 0 {
		return gitdir[:i]
	}
	return dir
}

func abbreviateHome(p, home string) string {
	switch {
	case home == "":
		return p
	case p == home:
		return "~"
	case strings.HasPrefix(p, home+string(filepath.Separator)):
		return "~" + p[len(home):]
	}
	return p
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
