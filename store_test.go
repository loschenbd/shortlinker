package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestStore is a store whose links are content, written in the text
// format ("slug = target | phone" lines), all kept.
func newTestStore(t *testing.T, content string) *Store {
	t.Helper()
	return newSplitStore(t, content, "")
}

// newSplitStore is a store with kept links and links that aren't kept (as
// sl-link makes them), each written in the text format.
func newSplitStore(t *testing.T, kept, auto string) *Store {
	t.Helper()
	s := &Store{Path: filepath.Join(t.TempDir(), LinksFile), Now: func() time.Time { return testNow }}
	var recs []Record
	for _, part := range []struct {
		text string
		kept bool
	}{{kept, true}, {auto, false}} {
		entries, _ := parseEntries(part.text)
		for _, e := range entries {
			origin := map[bool]string{true: "import", false: "sl-link"}[part.kept]
			recs = append(recs, Record{Slug: e.Slug, Target: e.Target, Phone: e.Phone, Kept: part.kept, Created: testNow, Origin: origin})
		}
	}
	if len(recs) > 0 {
		writeRecords(t, s.Path, recs...)
	}
	return s
}

func writeRecords(t *testing.T, path string, recs ...Record) {
	t.Helper()
	var lines []string
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	if err := writeLines(path, lines); err != nil {
		t.Fatal(err)
	}
}

var testNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

// textOf is the links in use that are kept (or aren't), in the text format.
func textOf(t *testing.T, s *Store, kept bool) string {
	t.Helper()
	entries, err := s.All()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.Rec.Kept == kept {
			b.WriteString(formatLine(e.Slug, e.Target, e.Phone) + "\n")
		}
	}
	return b.String()
}

// autoText is the links in use that aren't kept, in the text format.
func autoText(t *testing.T, s *Store) string { t.Helper(); return textOf(t, s, false) }

// recOf is slug's record, in any state.
func recOf(t *testing.T, s *Store, slug string) *Record {
	t.Helper()
	e, ok, err := s.Find(slug)
	if err != nil || !ok {
		t.Fatalf("no record for %q (ok=%v, err=%v)", slug, ok, err)
	}
	return e.Rec
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func slugsOf(entries []Entry) string {
	var s []string
	for _, e := range entries {
		s = append(s, e.Slug)
	}
	return strings.Join(s, ",")
}

const testHome = "/Users/test"

func TestStoreAllMissingFiles(t *testing.T) {
	s := newTestStore(t, "")
	links, err := s.All()
	if err != nil {
		t.Fatalf("All() on missing files: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("links = %v, want empty", links)
	}
}

func TestStoreListsKeptLinksFirst(t *testing.T) {
	s := newSplitStore(t, "budget = ~/b.numbers\ndaily = obsidian://daily\n", "a-md = ~/x/a.md\n")
	// Records in any order; kept ones list first, each group in file order.
	writeRecords(t, s.Path,
		Record{Slug: "a-md", Target: "~/x/a.md", Created: testNow},
		Record{Slug: "budget", Target: "~/b.numbers", Kept: true, Created: testNow},
		Record{Slug: "daily", Target: "obsidian://daily", Kept: true, Created: testNow})
	links, err := s.All()
	if err != nil {
		t.Fatal(err)
	}
	if got := slugsOf(links); got != "budget,daily,a-md" {
		t.Errorf("slugs = %s, want budget,daily,a-md", got)
	}
}

func TestStoreGet(t *testing.T) {
	s := newTestStore(t, "budget = ~/Documents/budget.numbers\n")
	target, ok, err := s.Get("budget")
	if err != nil || !ok || target != "~/Documents/budget.numbers" {
		t.Errorf("Get(budget) = %q, %v, %v", target, ok, err)
	}
	if _, ok, _ := s.Get("nope"); ok {
		t.Error("Get(nope) reported ok")
	}
}

func TestStoreFirstRecordWinsForARepeatedSlug(t *testing.T) {
	s := newTestStore(t, "")
	writeRecords(t, s.Path, Record{Slug: "x", Target: "/first", Created: testNow}, Record{Slug: "x", Target: "/second", Created: testNow})
	if target, _, _ := s.Get("x"); target != "/first" {
		t.Errorf("Get(x) = %q, want /first", target)
	}
	if err := s.Set("x", "/edited", testHome, Meta{}); err != nil {
		t.Fatal(err)
	}
	if target, _, _ := s.Get("x"); target != "/edited" {
		t.Errorf("after Set, Get(x) = %q, want /edited", target)
	}
}

func TestStoreSetMakesAKeptLink(t *testing.T) {
	s := newTestStore(t, "budget = /old\n")
	if err := s.Set("daily", "obsidian://daily", testHome, Meta{Origin: "manage", Note: "my journal"}); err != nil {
		t.Fatal(err)
	}
	r := recOf(t, s, "daily")
	if !r.Kept || r.Origin != "manage" || r.Note != "my journal" || !r.Created.Equal(testNow) || r.Linked != 0 {
		t.Errorf("record = %+v", r)
	}
	if got := textOf(t, s, true); got != "budget = /old\ndaily = obsidian://daily\n" {
		t.Errorf("kept links = %q", got)
	}
}

func TestStoreSetCreatesFileAndParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", LinksFile)
	s := &Store{Path: path}
	if err := s.Set("budget", "/x", testHome, Meta{}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); !strings.Contains(got, `"slug":"budget"`) || !strings.Contains(got, `"kept":true`) {
		t.Errorf("file = %q", got)
	}
}

func TestStoreFilesArePrivate(t *testing.T) {
	s := newTestStore(t, "")
	if err := s.Set("budget", "/x", testHome, Meta{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClick(Click{Slug: "budget", Route: "redirect"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.Path, s.clicksPath()} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode = %v, %v; want 0600", filepath.Base(p), fi.Mode().Perm(), err)
		}
	}
	// Tighten fixes a file saved before writes were private.
	os.Chmod(s.Path, 0o644)
	s.Tighten()
	if fi, _ := os.Stat(s.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("after Tighten: mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestStoreSetRejectsInvalid(t *testing.T) {
	s := newTestStore(t, "")
	for _, c := range []struct {
		slug, target string
		meta         Meta
	}{{"Bad Slug", "/x", Meta{}}, {"api", "/x", Meta{}}, {"ok", "", Meta{}}, {"ok", "/x", Meta{Origin: "evil"}}} {
		if err := s.Set(c.slug, c.target, testHome, c.meta); err == nil {
			t.Errorf("Set(%q, %q, %+v) succeeded", c.slug, c.target, c.meta)
		}
	}
}

func TestStoreSetFailsOnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod won't block reads")
	}
	s := newTestStore(t, "budget = /x\n")
	before := read(t, s.Path)
	if err := os.Chmod(s.Path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.Path, 0o600) }) // so the temp dir can be removed
	if err := s.Set("new", "/y", testHome, Meta{}); err == nil {
		t.Fatal("Set should fail on an unreadable file, but succeeded")
	}
	os.Chmod(s.Path, 0o600)
	if read(t, s.Path) != before {
		t.Error("the file changed")
	}
}

func TestStoreSetKeepsALinkAndItsRecord(t *testing.T) {
	s := newSplitStore(t, "budget = /b\n", "notes-md = ~/x/notes.md | https://sb/notes\nother-md = ~/x/other.md\n")
	if err := s.Set("notes-md", "~/x/notes.md", testHome, Meta{}); err != nil {
		t.Fatal(err)
	}
	r := recOf(t, s, "notes-md")
	if !r.Kept || r.Phone != "https://sb/notes" || r.Origin != "sl-link" {
		t.Errorf("record = %+v; want it kept, with its phone page and where it came from", r)
	}
	if got := autoText(t, s); got != "other-md = ~/x/other.md\n" {
		t.Errorf("links not kept = %q", got)
	}
	// Pointing it at another file drops the phone page.
	if err := s.Set("notes-md", "~/x/elsewhere.md", testHome, Meta{}); err != nil {
		t.Fatal(err)
	}
	if r := recOf(t, s, "notes-md"); r.Phone != "" {
		t.Errorf("phone page kept for a different file: %q", r.Phone)
	}
}

func TestStoreDeleteLeavesTombstones(t *testing.T) {
	s := newSplitStore(t, "budget = /x\ndaily = obsidian://daily\n", "a-md = /a.md\n")
	for _, slug := range []string{"budget", "a-md"} {
		if err := s.Delete(slug); err != nil {
			t.Fatal(err)
		}
		if r := recOf(t, s, slug); r.Removed == nil || r.Removed.Why != "deleted" || r.Kept {
			t.Errorf("%s = %+v; want a tombstone, not kept", slug, r)
		}
	}
	if links, _ := s.All(); slugsOf(links) != "daily" {
		t.Errorf("left = %s, want daily", slugsOf(links))
	}
	for _, slug := range []string{"nope", "budget"} {
		if err := s.Delete(slug); err == nil {
			t.Errorf("Delete(%q) succeeded, want an error", slug)
		}
	}
}

func TestStoreRename(t *testing.T) {
	s := newSplitStore(t, "budget = /b\n", "a-md = /a.md | https://sb/a\n")
	if err := s.Rename("a-md", "plan"); err != nil {
		t.Fatal(err)
	}
	if r := recOf(t, s, "plan"); r.Target != "/a.md" || r.Phone != "https://sb/a" || !r.Kept {
		t.Errorf("plan = %+v; want the same link under its new name, kept", r)
	}
	if _, ok, _ := s.Lookup("a-md"); ok {
		t.Error("the old slug still works as a link")
	}
	for _, c := range [][2]string{{"plan", "budget"}, {"plan", "Bad"}, {"plan", "f"}, {"nope", "fine"}, {"plan", "a-md"}} {
		if err := s.Rename(c[0], c[1]); err == nil {
			t.Errorf("Rename(%q, %q): want an error", c[0], c[1])
		}
	}
}

func TestStoreEnsureNamesNewLinkAfterFile(t *testing.T) {
	s := newTestStore(t, "budget = ~/Documents/budget.numbers\n")
	slug, created, err := s.Ensure("~/Documents/quarterly-report-2.pdf", testHome, Meta{})
	if err != nil || !created || slug != "quarterly-report-2-pdf" {
		t.Fatalf("Ensure = %q, %v, %v", slug, created, err)
	}
	if got, want := autoText(t, s), "quarterly-report-2-pdf = ~/Documents/quarterly-report-2.pdf\n"; got != want {
		t.Errorf("links not kept = %q, want %q", got, want)
	}
	if got := textOf(t, s, true); got != "budget = ~/Documents/budget.numbers\n" {
		t.Errorf("kept links changed: %q", got)
	}
}

func TestStoreEnsureReusesExistingLinkForSamePath(t *testing.T) {
	s := newSplitStore(t, "budget = ~/Documents/budget.numbers\n", "money = /Users/test/Documents/budget.numbers\n")
	for _, target := range []string{"~/Documents/budget.numbers", "/Users/test/Documents/./budget.numbers"} {
		slug, created, err := s.Ensure(target, testHome, Meta{})
		if err != nil || created || slug != "budget" {
			t.Errorf("Ensure(%q) = %q, %v, %v; want budget (the kept one), false", target, slug, created, err)
		}
	}
	if links, _ := s.All(); len(links) != 2 {
		t.Errorf("links = %v, want the 2 originals only", links)
	}
}

func TestStoreEnsureAvoidsTakenAndReservedSlugs(t *testing.T) {
	s := newSplitStore(t, "notes-md = ~/a/notes.md\nnotes-md-2 = obsidian://x\n", "b-notes-md = ~/c/b/notes.md\n")
	// Run in a fixed order: each new link takes a slug the next might want.
	for _, c := range [][2]string{
		{"~/b/notes.md", "notes-md-3"},
		{"~/d/notes.md", "d-notes-md"},
		{"/srv/api", "srv-api"},
		{"/api", "api-2"},
		{"~/e/TODO.md", "todo-md"},
		{"~/f/TODO.md", "f-todo-md"},
	} {
		slug, _, err := s.Ensure(c[0], testHome, Meta{})
		if err != nil || slug != c[1] {
			t.Errorf("Ensure(%q) = %q, %v; want %q", c[0], slug, err, c[1])
		}
	}
}

func TestStoreEnsureRejectsURLsAndEmptyTargets(t *testing.T) {
	s := newTestStore(t, "")
	for _, target := range []string{"", "https://example.com"} {
		if _, _, err := s.Ensure(target, testHome, Meta{}); err == nil {
			t.Errorf("Ensure(%q): want an error", target)
		}
	}
}

func TestStoreConcurrentWritesKeepEveryLink(t *testing.T) {
	s := newTestStore(t, "")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			if _, _, err := s.Ensure(fmt.Sprintf("~/f/file%d.md", i), testHome, Meta{}); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := s.Set(fmt.Sprintf("mine-%d", i), "/x", testHome, Meta{}); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := s.RecordClick(Click{Slug: "mine-0", Route: "open"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if links, _ := s.All(); len(links) != 40 {
		t.Errorf("got %d links, want 40", len(links))
	}
}

func TestStorePruneLeavesKeptAndLiveLinks(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.md")
	if err := os.WriteFile(live, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	auto := "live = " + live + "\ngone = /nope/gone.md\nurl = https://example.com\ngone-md = /nope/gone2.md | https://sb/gone\n"
	s := newSplitStore(t, "hand = /nope/hand.md\n", auto)
	before := autoText(t, s)
	removed, err := s.Prune(testHome, true)
	if err != nil || strings.Join(removed, ",") != "gone,gone-md" {
		t.Fatalf("dry run = %v, %v", removed, err)
	}
	if autoText(t, s) != before {
		t.Errorf("dry run changed the links")
	}
	if removed, err = s.Prune(testHome, false); err != nil || strings.Join(removed, ",") != "gone,gone-md" {
		t.Fatalf("prune = %v, %v", removed, err)
	}
	if got, want := autoText(t, s), "live = "+live+"\nurl = https://example.com\n"; got != want {
		t.Errorf("left =\n%s\nwant\n%s", got, want)
	}
	if r := recOf(t, s, "gone"); r.Removed == nil || r.Removed.Why != "pruned" {
		t.Errorf("gone should be a tombstone marked pruned, got %+v", r.Removed)
	}
	if r := recOf(t, s, "hand"); r.Removed != nil {
		t.Error("prune removed a kept link")
	}
}

func TestStoreWriteFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", LinksFile)
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, real, Record{Slug: "a", Target: "~/a", Kept: true, Created: testNow})
	link := filepath.Join(dir, LinksFile)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: link}
	if err := s.Set("b", "~/b", testHome, Meta{}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is no longer a symlink", LinksFile)
	}
	if got := read(t, real); !strings.Contains(got, `"slug":"a"`) || !strings.Contains(got, `"slug":"b"`) {
		t.Errorf("real file = %q", got)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "dotfiles", ".links-*")); len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestStoreReadersNeverSeeHalfAFile(t *testing.T) {
	var b strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&b, "link-%d = ~/Documents/some/long/path/to/file-%d.md\n", i, i)
	}
	s := newTestStore(t, b.String())
	reader := &Store{Path: s.Path} // a second store reads from disk every time the file changes
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 40 {
			if err := s.Set(fmt.Sprintf("link-%d", i), fmt.Sprintf("~/moved/%d.md", i), testHome, Meta{}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for reads := 0; ; reads++ {
		select {
		case <-done:
			return
		default:
		}
		links, err := reader.All()
		if err != nil {
			t.Fatal(err)
		}
		if len(links) != 3000 {
			t.Fatalf("read %d: saw %d links mid-save, want 3000", reads, len(links))
		}
	}
}

func TestStorePhonePages(t *testing.T) {
	s := newSplitStore(t, "pipe = ~/a | b.md\nweb = obsidian://x\n", "notes-md = ~/x/notes.md\n")
	if err := s.SetPhone("notes-md", "https://sb.example:3000/notes"); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := s.Lookup("notes-md"); l.Target != "~/x/notes.md" || l.Phone != "https://sb.example:3000/notes" {
		t.Errorf("Lookup(notes-md) = %+v", l)
	}
	if l, _, _ := s.Lookup("pipe"); l.Target != "~/a | b.md" || l.Phone != "" {
		t.Errorf("a path with \" | \" was split: %+v", l)
	}
	if slug, created, _ := s.Ensure("/Users/test/x/notes.md", testHome, Meta{}); slug != "notes-md" || created {
		t.Errorf("Ensure matched %q, created = %v; want notes-md", slug, created)
	}
	for _, c := range [][2]string{{"web", "https://x"}, {"notes-md", "javascript:alert(1)"}, {"nope", "https://x"}} {
		if err := s.SetPhone(c[0], c[1]); err == nil {
			t.Errorf("SetPhone(%q, %q): want an error", c[0], c[1])
		}
	}
}
