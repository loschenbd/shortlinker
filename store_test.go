package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newTestStore is a store whose links.txt holds content and which has no
// sl-link links yet.
func newTestStore(t *testing.T, content string) *Store {
	t.Helper()
	return newSplitStore(t, content, "")
}

// newSplitStore is a store with your links and sl-link's.
func newSplitStore(t *testing.T, hand, auto string) *Store {
	t.Helper()
	dir := t.TempDir()
	s := &Store{Path: filepath.Join(dir, "links.txt")}
	for path, content := range map[string]string{s.Path: hand, s.autoPath(): auto} {
		if content != "" {
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s
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

var autoHead = strings.Join(autoHeader, "\n") + "\n"

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

func TestStoreAllReadsYoursThenSlLinks(t *testing.T) {
	s := newSplitStore(t, "# mine\nbudget = ~/b.numbers\nshared = ~/mine.md\n", "a-md = ~/x/a.md\nshared = ~/theirs.md\n")
	links, err := s.All()
	if err != nil {
		t.Fatal(err)
	}
	if got := slugsOf(links); got != "budget,shared,a-md" {
		t.Errorf("slugs = %s, want budget,shared,a-md (yours win a clash)", got)
	}
	if l, _, _ := s.Lookup("shared"); l.Target != "~/mine.md" || l.Auto {
		t.Errorf("Lookup(shared) = %+v, want yours", l)
	}
	if l, _, _ := s.Lookup("a-md"); !l.Auto {
		t.Errorf("Lookup(a-md) = %+v, want an sl-link link", l)
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

func TestStoreFirstLineWinsForARepeatedSlug(t *testing.T) {
	s := newTestStore(t, "x = /first\nx = /second\n")
	if target, _, _ := s.Get("x"); target != "/first" {
		t.Errorf("Get(x) = %q, want /first", target)
	}
	// An edit changes the line reads use.
	if err := s.Set("x", "/edited"); err != nil {
		t.Fatal(err)
	}
	if target, _, _ := s.Get("x"); target != "/edited" {
		t.Errorf("after Set, Get(x) = %q, want /edited", target)
	}
}

func TestStoreSetAppendsNewSlug(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = /old\n")
	if err := s.Set("daily", "obsidian://daily"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, s.Path), "# my links\nbudget = /old\ndaily = obsidian://daily\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestStoreSetUpdatesInPlacePreservingCommentsAndOrder(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = /old/path\nBad Line\ndaily = obsidian://daily\n")
	if err := s.Set("budget", "/new/path"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, s.Path), "# my links\nbudget = /new/path\nBad Line\ndaily = obsidian://daily\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestStoreSetCreatesFileAndParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "links.txt")
	s := &Store{Path: path}
	if err := s.Set("budget", "/x"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "budget = /x\n" {
		t.Errorf("file = %q", got)
	}
}

func TestStoreFilesArePrivate(t *testing.T) {
	s := newTestStore(t, "")
	if err := s.Set("budget", "/x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Ensure("/y/a.md", "/Users/test"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{s.Path, s.autoPath()} {
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
	if err := s.Set("Bad Slug", "/x"); err == nil {
		t.Error("Set with invalid slug succeeded")
	}
	if err := s.Set("api", "/x"); err == nil {
		t.Error("Set with reserved slug succeeded")
	}
	if err := s.Set("ok", ""); err == nil {
		t.Error("Set with empty target succeeded")
	}
}

func TestStoreSetFailsOnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod won't block reads")
	}
	s := newTestStore(t, "budget = /x\n")
	if err := os.Chmod(s.Path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.Path, 0o600) }) // ensure temp dir is removable
	if err := s.Set("new", "/y"); err == nil {
		t.Fatal("Set should fail on unreadable file, but succeeded")
	}
	if err := os.Chmod(s.Path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s.Path); got != "budget = /x\n" {
		t.Errorf("file content changed unexpectedly: %q", got)
	}
}

func TestStoreSetMovesAnSlLinkToYours(t *testing.T) {
	s := newSplitStore(t, "budget = /b\n", autoHead+"notes-md = ~/x/notes.md | https://sb/notes\nother-md = ~/x/other.md\n")
	if err := s.Set("notes-md", "~/x/notes.md"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, s.Path), "budget = /b\nnotes-md = ~/x/notes.md | https://sb/notes\n"; got != want {
		t.Errorf("links.txt = %q, want %q (the phone page kept: same file)", got, want)
	}
	if got, want := read(t, s.autoPath()), autoHead+"other-md = ~/x/other.md\n"; got != want {
		t.Errorf("auto-links.txt = %q, want %q", got, want)
	}
	if l, _, _ := s.Lookup("notes-md"); l.Auto {
		t.Error("notes-md is still sl-link's")
	}
}

func TestStoreDelete(t *testing.T) {
	s := newSplitStore(t, "# keep\nbudget = /x\ndaily = obsidian://daily\n", autoHead+"a-md = /a.md\n")
	if err := s.Delete("budget"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, s.Path), "# keep\ndaily = obsidian://daily\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	if err := s.Delete("a-md"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, s.autoPath()); got != autoHead {
		t.Errorf("auto-links.txt = %q, want just the header", got)
	}
	if err := s.Delete("nope"); err == nil {
		t.Error("Delete(nope) succeeded, want error")
	}
}

func TestStoreRename(t *testing.T) {
	s := newSplitStore(t, "budget = /b\n", autoHead+"a-md = /a.md | https://sb/a\n")
	if err := s.Rename("a-md", "plan"); err != nil {
		t.Fatal(err)
	}
	if l, ok, _ := s.Lookup("plan"); !ok || l.Target != "/a.md" || l.Phone != "https://sb/a" || !l.Auto {
		t.Errorf("Lookup(plan) = %+v, %v; want the same link under its new name", l, ok)
	}
	if _, ok, _ := s.Lookup("a-md"); ok {
		t.Error("the old slug still works")
	}
	for _, c := range [][2]string{{"plan", "budget"}, {"plan", "Bad"}, {"plan", "f"}, {"nope", "fine"}} {
		if err := s.Rename(c[0], c[1]); err == nil {
			t.Errorf("Rename(%q, %q): want an error", c[0], c[1])
		}
	}
}

func TestStoreEnsureNamesNewLinkAfterFile(t *testing.T) {
	s := newTestStore(t, "budget = ~/Documents/budget.numbers\n")
	slug, created, err := s.Ensure("~/Documents/quarterly-report-2.pdf", "/Users/test")
	if err != nil || !created || slug != "quarterly-report-2-pdf" {
		t.Fatalf("Ensure = %q, %v, %v", slug, created, err)
	}
	if got, want := read(t, s.autoPath()), autoHead+"quarterly-report-2-pdf = ~/Documents/quarterly-report-2.pdf\n"; got != want {
		t.Errorf("auto-links.txt = %q, want %q", got, want)
	}
	if got := read(t, s.Path); got != "budget = ~/Documents/budget.numbers\n" {
		t.Errorf("links.txt changed: %q", got)
	}
}

func TestStoreEnsureReusesExistingLinkForSamePath(t *testing.T) {
	s := newSplitStore(t, "budget = ~/Documents/budget.numbers\n", "money = /Users/test/Documents/budget.numbers\n")
	for _, target := range []string{"~/Documents/budget.numbers", "/Users/test/Documents/./budget.numbers"} {
		slug, created, err := s.Ensure(target, "/Users/test")
		if err != nil || created || slug != "budget" {
			t.Errorf("Ensure(%q) = %q, %v, %v; want budget, false", target, slug, created, err)
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
		slug, _, err := s.Ensure(c[0], "/Users/test")
		if err != nil || slug != c[1] {
			t.Errorf("Ensure(%q) = %q, %v; want %q", c[0], slug, err, c[1])
		}
	}
}

func TestStoreEnsureRejectsURLsAndEmptyTargets(t *testing.T) {
	s := newTestStore(t, "")
	for _, target := range []string{"", "https://example.com"} {
		if _, _, err := s.Ensure(target, "/Users/test"); err == nil {
			t.Errorf("Ensure(%q): want an error", target)
		}
	}
}

func TestStoreConcurrentWritesKeepEveryLink(t *testing.T) {
	s := newTestStore(t, "")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, _, err := s.Ensure(fmt.Sprintf("~/f/file%d.md", i), "/Users/test"); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := s.Set(fmt.Sprintf("mine-%d", i), "/x"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if links, _ := s.All(); len(links) != 40 {
		t.Errorf("got %d links, want 40", len(links))
	}
}

func TestStorePruneAutoKeepsYoursAndLiveLinks(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.md")
	if err := os.WriteFile(live, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	auto := autoHead + "live = " + live + "\n# a note\ngone = /nope/gone.md\nurl = https://example.com\ngone-md = /nope/gone2.md | https://sb/gone\n"
	s := newSplitStore(t, "hand = /nope/hand.md\n", auto)
	removed, err := s.PruneAuto("/Users/test", true)
	if err != nil || strings.Join(removed, ",") != "gone,gone-md" {
		t.Fatalf("dry run = %v, %v", removed, err)
	}
	if read(t, s.autoPath()) != auto {
		t.Errorf("dry run changed the file")
	}
	if removed, err = s.PruneAuto("/Users/test", false); err != nil || strings.Join(removed, ",") != "gone,gone-md" {
		t.Fatalf("prune = %v, %v", removed, err)
	}
	want := autoHead + "live = " + live + "\n# a note\nurl = https://example.com\n"
	if got := read(t, s.autoPath()); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if read(t, s.Path) != "hand = /nope/hand.md\n" {
		t.Error("prune touched links.txt")
	}
}

func TestStoreWriteFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "links.txt")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("a = ~/a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "links.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s := &Store{Path: link}
	if err := s.Set("b", "~/b"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("links.txt is no longer a symlink")
	}
	if got := read(t, real); got != "a = ~/a\nb = ~/b\n" {
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 40 {
			if err := s.Set(fmt.Sprintf("link-%d", i), fmt.Sprintf("~/moved/%d.md", i)); err != nil {
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
		links, err := s.All()
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
	if l, _, _ := s.Lookup("notes-md"); l.Target != "~/x/notes.md" || l.Phone != "https://sb.example:3000/notes" || !l.Auto {
		t.Errorf("Lookup(notes-md) = %+v", l)
	}
	if l, _, _ := s.Lookup("pipe"); l.Target != "~/a | b.md" || l.Phone != "" {
		t.Errorf("a path with \" | \" was split: %+v", l)
	}
	if slug, created, _ := s.Ensure("/Users/test/x/notes.md", "/Users/test"); slug != "notes-md" || created {
		t.Errorf("Ensure matched %q, created = %v; want notes-md", slug, created)
	}
	if err := s.Set("notes-md", "~/x/other.md"); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := s.Lookup("notes-md"); l.Phone != "" {
		t.Error("pointing the link at another file kept the old phone page")
	}
	for _, c := range [][2]string{{"web", "https://x"}, {"notes-md", "javascript:alert(1)"}, {"nope", "https://x"}} {
		if err := s.SetPhone(c[0], c[1]); err == nil {
			t.Errorf("SetPhone(%q, %q): want an error", c[0], c[1])
		}
	}
}

// The layout before sl-link had a file of its own: its links at the end of
// links.txt, under a marker comment.
const oldMarker = "# Added by sl-link. `shortlinker prune` removes the ones whose file is gone."

func TestStoreMigrateMovesTheOldSection(t *testing.T) {
	before := "# mine\nbudget = ~/b.numbers\n\n" + oldMarker + "\na-md = ~/x/a.md\n# a note\nb-md = ~/x/b.md | https://sb/b\nBad Line\n"
	s := newTestStore(t, before)
	before2, _ := s.All()
	moved, err := s.Migrate()
	if err != nil || moved != 2 {
		t.Fatalf("Migrate = %d, %v; want 2", moved, err)
	}
	if got := read(t, s.Path); got != "# mine\nbudget = ~/b.numbers\n" {
		t.Errorf("links.txt = %q", got)
	}
	if got, want := read(t, s.autoPath()), autoHead+"a-md = ~/x/a.md\n# a note\nb-md = ~/x/b.md | https://sb/b\nBad Line\n"; got != want {
		t.Errorf("auto-links.txt = %q, want %q", got, want)
	}
	if got := read(t, s.Path+".before-split"); got != before {
		t.Errorf("backup = %q, want the original", got)
	}
	after, _ := s.All()
	same := func(es []Entry) string {
		var b strings.Builder
		for _, e := range es {
			fmt.Fprintf(&b, "%s=%s|%s;", e.Slug, e.Target, e.Phone)
		}
		return b.String()
	}
	if same(before2) != same(after) {
		t.Errorf("links changed:\nbefore %v\nafter  %v", before2, after)
	}
	if l, _, _ := s.Lookup("a-md"); !l.Auto {
		t.Error("a-md isn't sl-link's after the move")
	}
	if moved, err := s.Migrate(); err != nil || moved != 0 {
		t.Errorf("second Migrate = %d, %v; want 0", moved, err)
	}
}

// A crash after writing sl-link's file but before trimming links.txt leaves
// both holding the links; running again finishes without duplicating them.
func TestStoreMigrateFinishesAHalfDoneRun(t *testing.T) {
	s := newSplitStore(t, "budget = /b\n\n"+oldMarker+"\na-md = /a.md\nb-md = /b.md\n", autoHead+"a-md = /a.md\n")
	moved, err := s.Migrate()
	if err != nil || moved != 1 {
		t.Fatalf("Migrate = %d, %v; want 1 (b-md)", moved, err)
	}
	if got, want := read(t, s.autoPath()), autoHead+"a-md = /a.md\nb-md = /b.md\n"; got != want {
		t.Errorf("auto-links.txt = %q, want %q", got, want)
	}
	if got := read(t, s.Path); got != "budget = /b\n" {
		t.Errorf("links.txt = %q", got)
	}
}

func TestStoreSetStaysAboveAnOldSectionNotYetMoved(t *testing.T) {
	s := newTestStore(t, "budget = /b\n\n"+oldMarker+"\na-md = /a.md\n")
	if err := s.Set("daily", "obsidian://daily"); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, s.Path), "budget = /b\ndaily = obsidian://daily\n\n"+oldMarker+"\na-md = /a.md\n"; got != want {
		t.Errorf("links.txt = %q, want %q", got, want)
	}
}
