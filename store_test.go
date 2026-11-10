package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestStore(t *testing.T, content string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "links.txt")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Store{Path: path}
}

func TestStoreAllMissingFile(t *testing.T) {
	s := newTestStore(t, "")
	links, err := s.All()
	if err != nil {
		t.Fatalf("All() on missing file: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("links = %v, want empty", links)
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

func TestStoreSetAppendsNewSlug(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = /old\n")
	if err := s.Set("daily", "obsidian://daily"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# my links\nbudget = /old\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
}

func TestStoreSetUpdatesInPlacePreservingCommentsAndOrder(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = /old/path\ndaily = obsidian://daily\n")
	if err := s.Set("budget", "/new/path"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# my links\nbudget = /new/path\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
}

func TestStoreSetCreatesFileAndParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "links.txt")
	s := &Store{Path: path}
	if err := s.Set("budget", "/x"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "budget = /x\n" {
		t.Errorf("file = %q", data)
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
	t.Cleanup(func() { os.Chmod(s.Path, 0o644) }) // ensure temp dir is removable
	if err := s.Set("new", "/y"); err == nil {
		t.Fatal("Set should fail on unreadable file, but succeeded")
	}
	if err := os.Chmod(s.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	if string(data) != "budget = /x\n" {
		t.Errorf("file content changed unexpectedly: %q", data)
	}
}

func TestStoreDelete(t *testing.T) {
	s := newTestStore(t, "# keep\nbudget = /x\ndaily = obsidian://daily\n")
	if err := s.Delete("budget"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# keep\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	if err := s.Delete("nope"); err == nil {
		t.Error("Delete(nope) succeeded, want error")
	}
}

func TestStoreEnsureNamesNewLinkAfterFile(t *testing.T) {
	s := newTestStore(t, "budget = ~/Documents/budget.numbers\n")
	slug, created, err := s.Ensure("~/Documents/quarterly-report-2.pdf", "/Users/test")
	if err != nil || !created || slug != "quarterly-report-2-pdf" {
		t.Fatalf("Ensure = %q, %v, %v", slug, created, err)
	}
	if target, ok, _ := s.Get(slug); !ok || target != "~/Documents/quarterly-report-2.pdf" {
		t.Errorf("Get(%q) = %q, %v", slug, target, ok)
	}
}

func TestStoreEnsureReusesExistingLinkForSamePath(t *testing.T) {
	s := newTestStore(t, "budget = ~/Documents/budget.numbers\nmoney = /Users/test/Documents/budget.numbers\n")
	for _, target := range []string{"~/Documents/budget.numbers", "/Users/test/Documents/./budget.numbers"} {
		slug, created, err := s.Ensure(target, "/Users/test")
		if err != nil || created || slug != "budget" {
			t.Errorf("Ensure(%q) = %q, %v, %v; want budget, false", target, slug, created, err)
		}
	}
	links, _ := s.All()
	if len(links) != 2 {
		t.Errorf("links = %v, want the 2 originals only", links)
	}
}

func TestStoreEnsureAvoidsTakenAndReservedSlugs(t *testing.T) {
	s := newTestStore(t, "notes-md = ~/a/notes.md\nb-notes-md = ~/c/b/notes.md\nnotes-md-2 = obsidian://x\n")
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Ensure(fmt.Sprintf("~/f/file%d.md", i), "/Users/test"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if links, _ := s.All(); len(links) != 20 {
		t.Errorf("got %d links, want 20", len(links))
	}
}

func TestStoreEnsureStartsAutoSectionAndSetStaysAboveIt(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = ~/b.numbers\n")
	if _, _, err := s.Ensure("~/x/a.md", "/Users/test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Ensure("~/x/b.md", "/Users/test"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("daily", "obsidian://daily"); err != nil {
		t.Fatal(err)
	}
	want := "# my links\nbudget = ~/b.numbers\ndaily = obsidian://daily\n\n" + autoMarker + "\na-md = ~/x/a.md\nb-md = ~/x/b.md\n"
	if got, _ := os.ReadFile(s.Path); string(got) != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	for slug, wantAuto := range map[string]bool{"budget": false, "daily": false, "a-md": true} {
		if l, ok, _ := s.Lookup(slug); !ok || l.Auto != wantAuto {
			t.Errorf("Lookup(%s): auto = %v, ok = %v; want auto = %v", slug, l.Auto, ok, wantAuto)
		}
	}
}

func TestStorePruneAutoKeepsHandMadeAndLiveLinks(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.md")
	if err := os.WriteFile(live, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	content := "hand = /nope/hand.md\n\n" + autoMarker + "\nlive = " + live + "\n# a note\ngone = /nope/gone.md\nurl = https://example.com\n"
	s := newTestStore(t, content)
	removed, err := s.PruneAuto("/Users/test", true)
	if err != nil || strings.Join(removed, ",") != "gone" {
		t.Fatalf("dry run = %v, %v", removed, err)
	}
	if got, _ := os.ReadFile(s.Path); string(got) != content {
		t.Errorf("dry run changed the file")
	}
	if removed, err = s.PruneAuto("/Users/test", false); err != nil || strings.Join(removed, ",") != "gone" {
		t.Fatalf("prune = %v, %v", removed, err)
	}
	want := strings.Replace(content, "gone = /nope/gone.md\n", "", 1)
	if got, _ := os.ReadFile(s.Path); string(got) != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if removed, _ := newTestStore(t, "hand = /nope/x\n").PruneAuto("/Users/test", false); len(removed) != 0 {
		t.Errorf("no sl-link section: removed %v", removed)
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
	if got, _ := os.ReadFile(real); string(got) != "a = ~/a\nb = ~/b\n" {
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
	s := newTestStore(t, "pipe = ~/a | b.md\nweb = obsidian://x\n\n"+autoMarker+"\nnotes-md = ~/x/notes.md\n")
	if err := s.SetPhone("notes-md", "https://sb.ts.net:3000/notes"); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := s.Lookup("notes-md"); l.Target != "~/x/notes.md" || l.Phone != "https://sb.ts.net:3000/notes" || !l.Auto {
		t.Errorf("Lookup(notes-md) = %+v", l)
	}
	if l, _, _ := s.Lookup("pipe"); l.Target != "~/a | b.md" || l.Phone != "" {
		t.Errorf("a path with \" | \" was split: %+v", l)
	}
	if slug, created, _ := s.Ensure("/Users/test/x/notes.md", "/Users/test"); slug != "notes-md" || created {
		t.Errorf("Ensure matched %q, created = %v; want notes-md", slug, created)
	}
	if err := s.Set("notes-md", "~/x/notes.md"); err != nil {
		t.Fatal(err)
	}
	if l, _, _ := s.Lookup("notes-md"); l.Phone == "" {
		t.Error("re-setting the same target dropped the phone page")
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
	s2 := newTestStore(t, autoMarker+"\ngone-md = /nope/gone.md | https://sb/gone\n")
	if removed, _ := s2.PruneAuto("/Users/test", false); strings.Join(removed, ",") != "gone-md" {
		t.Errorf("prune with a phone page: removed %v", removed)
	}
}
