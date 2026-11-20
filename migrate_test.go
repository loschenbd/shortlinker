package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The layout before the merge: your links in links.txt, sl-link's records
// in auto-links.jsonl, and the click log.
func TestMigrateMergesLinksTxtIntoRecords(t *testing.T) {
	dir := t.TempDir()
	at := func(name string) string { return filepath.Join(dir, name) }
	hand := "# Work\n\n# the budget, for taxes\nbudget = ~/b.numbers\ndaily = obsidian://daily\nnotes = ~/n.md | https://sb/notes\nBad Line\nclash = ~/mine.md\n"
	if err := os.WriteFile(at(handFile), []byte(hand), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRecords(t, at(autoFile),
		Record{Slug: "a-md", Target: "~/x/a.md", Created: testNow, Origin: "sl-link", Session: "s1", Clicks: 2},
		Record{Slug: "clash", Target: "~/theirs.md", Created: testNow, Origin: "sl-link", Session: "s2", Clicks: 1},
		Record{Slug: "old", Target: "~/old.md", Created: testNow, Origin: "yours", Removed: &Removal{At: testNow, Why: "renamed", To: "budget"}})
	autoBefore := read(t, at(autoFile))
	var clicks strings.Builder
	for _, c := range []Click{{T: testNow, Slug: "daily", Route: "redirect", Device: "100.101.102.103"}, {T: testNow, Slug: "daily", Route: "redirect"}, {T: testNow, Slug: "budget", Route: "renamed"}} {
		b, _ := json.Marshal(c)
		clicks.WriteString(string(b) + "\n")
	}
	os.WriteFile(at(ClicksFile), []byte(clicks.String()), 0o600)

	// What every slug did before: links.txt wins a clash, as it did.
	want := map[string]string{"budget": "~/b.numbers|", "daily": "obsidian://daily|", "notes": "~/n.md|https://sb/notes", "clash": "~/mine.md|", "a-md": "~/x/a.md|"}

	s := &Store{Path: at(LinksFile), Now: (&clock{t: testNow}).now}
	m, err := s.Migrate(testHome)
	if err != nil || m != (Migration{FromAuto: 3, FromHand: 4}) {
		t.Fatalf("Migrate = %+v, %v", m, err)
	}
	all, _ := s.All()
	got := map[string]string{}
	for _, e := range all {
		got[e.Slug] = e.Target + "|" + e.Phone
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("links after:\n %v\nwant\n %v", got, want)
	}
	for slug, kept := range map[string]bool{"budget": true, "daily": true, "notes": true, "clash": true, "a-md": false} {
		if r := recOf(t, s, slug); r.Kept != kept {
			t.Errorf("%s kept = %v, want %v", slug, r.Kept, kept)
		}
	}
	if r := recOf(t, s, "budget"); r.Note != "the budget, for taxes" || r.Origin != "import" {
		t.Errorf("budget = %+v; want the comment above it as its note", r)
	}
	if r := recOf(t, s, "daily"); r.Note != "" || r.Clicks != 2 || r.LastDevice == "" {
		t.Errorf("daily = %+v; want 2 clicks from the log and no note (a heading isn't a note)", r)
	}
	if r := recOf(t, s, "clash"); r.Session != "s2" || r.Target != "~/mine.md" || r.Clicks != 1 {
		t.Errorf("clash = %+v; want links.txt's target with the record's history", r)
	}
	if r := recOf(t, s, "old"); r.Origin != "import" || r.Removed == nil {
		t.Errorf("old tombstone = %+v", r)
	}
	for name, before := range map[string]string{handFile + ".before-records": hand, autoFile + ".before-merge": autoBefore} {
		if read(t, at(name)) != before {
			t.Errorf("backup %s doesn't hold the original", name)
		}
	}
	for _, name := range []string{handFile, autoFile} {
		if _, err := os.Stat(at(name)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", name)
		}
	}
	if m, err := s.Migrate(testHome); err != nil || m.Any() {
		t.Errorf("second Migrate = %+v, %v", m, err)
	}
}

// The oldest layout: sl-link's links at the end of links.txt under a marker.
func TestMigrateTheOldestLayout(t *testing.T) {
	dir := t.TempDir()
	text := "budget = ~/b.numbers\n\n# Added by sl-link. `shortlinker prune` removes the ones whose file is gone.\na-md = ~/x/a.md\nb-md = ~/x/b.md | https://sb/b\n"
	os.WriteFile(filepath.Join(dir, handFile), []byte(text), 0o600)
	s := &Store{Path: filepath.Join(dir, LinksFile)}
	m, err := s.Migrate(testHome)
	if err != nil || m != (Migration{FromSplit: 2, FromHand: 1}) {
		t.Fatalf("Migrate = %+v, %v", m, err)
	}
	if got := textOf(t, s, true); got != "budget = ~/b.numbers\n" {
		t.Errorf("kept = %q", got)
	}
	if got := textOf(t, s, false); got != "a-md = ~/x/a.md\nb-md = ~/x/b.md | https://sb/b\n" {
		t.Errorf("not kept = %q", got)
	}
}

// A crash after saving links.jsonl but before renaming auto-links.jsonl
// leaves both; running again finishes without duplicating anything.
func TestMigrateFinishesAHalfDoneRun(t *testing.T) {
	dir := t.TempDir()
	recs := []Record{{Slug: "a-md", Target: "/a.md", Created: testNow}, {Slug: "b-md", Target: "/b.md", Created: testNow}}
	writeRecords(t, filepath.Join(dir, autoFile), recs...)
	writeRecords(t, filepath.Join(dir, LinksFile), recs[0])
	s := &Store{Path: filepath.Join(dir, LinksFile)}
	m, err := s.Migrate(testHome)
	if err != nil || m != (Migration{FromAuto: 1}) {
		t.Fatalf("Migrate = %+v, %v; want just b-md", m, err)
	}
	if all, _ := s.All(); slugsOf(all) != "a-md,b-md" {
		t.Errorf("links = %s", slugsOf(all))
	}
}
