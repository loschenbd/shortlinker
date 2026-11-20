package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clock is a settable Now for a store.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func days(n int) time.Duration           { return time.Duration(n) * 24 * time.Hour }
func withClock(s *Store) *clock          { c := &clock{t: testNow}; s.Now = c.now; return c }
func mustEnsure(t *testing.T, s *Store, target string, m Meta) string {
	t.Helper()
	slug, _, err := s.Ensure(target, "/Users/test", m)
	if err != nil {
		t.Fatalf("Ensure(%q): %v", target, err)
	}
	return slug
}

func TestEnsureRecordsWhoMadeALinkAndCountsRelinks(t *testing.T) {
	s := newTestStore(t, "")
	c := withClock(s)
	slug, created, err := s.Ensure("/y/plan.md", "/Users/test", Meta{Origin: "sl-link", Session: "0dae59d5-1e9f", Note: "  Q3 plan\nfor review "})
	if err != nil || !created || slug != "plan-md" {
		t.Fatalf("Ensure = %q, %v, %v", slug, created, err)
	}
	r := recOf(t, s, slug)
	if r.Origin != "sl-link" || r.Session != "0dae59d5-1e9f" || r.Note != "Q3 plan for review" || r.Linked != 1 || !r.Created.Equal(testNow) {
		t.Errorf("record = %+v", r)
	}
	c.advance(days(3))
	if again, created, _ := s.Ensure("/y/plan.md", "/Users/test", Meta{Origin: "sl-link"}); again != slug || created {
		t.Fatalf("relink = %q, %v; want the same link", again, created)
	}
	r = recOf(t, s, slug)
	if r.Linked != 2 || r.LastLinked == nil || !r.LastLinked.Equal(testNow.Add(days(3))) || r.Note != "Q3 plan for review" {
		t.Errorf("after relinking: linked %d, last %v, note %q", r.Linked, r.LastLinked, r.Note)
	}
}

func TestMetaIsChecked(t *testing.T) {
	s := newTestStore(t, "")
	for _, m := range []Meta{{Origin: "evil"}, {Session: "has space"}, {Session: strings.Repeat("a", 200)}} {
		if _, _, err := s.Ensure("/y/a.md", "/Users/test", m); err == nil {
			t.Errorf("Ensure with %+v: want an error", m)
		}
	}
	slug := mustEnsure(t, s, "/y/b.md", Meta{Note: strings.Repeat("é", 300)})
	if r := recOf(t, s, slug); len([]rune(r.Note)) != maxNote || r.Origin != "api" {
		t.Errorf("note is %d runes, origin %q; want %d and api", len([]rune(r.Note)), r.Origin, maxNote)
	}
}

func TestProjectIsTheMainCheckoutEvenFromAWorktree(t *testing.T) {
	home := t.TempDir()
	main := filepath.Join(home, "Code", "app")
	wt := filepath.Join(main, ".worktrees", "fix-nav")
	for _, d := range []string{filepath.Join(main, ".git", "worktrees", "fix-nav"), filepath.Join(wt, "docs"), filepath.Join(home, "Documents")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "fix-nav")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		filepath.Join(main, "README.md"):         "~/Code/app",
		filepath.Join(wt, "docs", "plan.md"):     "~/Code/app",
		filepath.Join(home, "Documents", "x.md"): "",
	} {
		if got := projectOf(path, home); got != want {
			t.Errorf("projectOf(%s) = %q, want %q", path, got, want)
		}
	}
	// A worktree that's been removed still belongs to its project.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if got := projectOf(filepath.Join(wt, "docs", "plan.md"), home); got != "~/Code/app" {
		t.Errorf("after removing the worktree, project = %q", got)
	}
}

func TestRetentionArchivesUnusedLinksAndUseBringsThemBack(t *testing.T) {
	s := newTestStore(t, "mine = /y/mine.md\n")
	c := withClock(s)
	slug := mustEnsure(t, s, "/y/a.md", Meta{})
	c.advance(days(30) - time.Second)
	if got, _ := s.Archive(); len(got) != 0 {
		t.Fatalf("archived %v a second early", got)
	}
	c.advance(time.Second)
	if got, _ := s.Archive(); strings.Join(got, ",") != slug {
		t.Fatalf("archived %v at 30 days, want %s", got, slug)
	}
	if all, _ := s.All(); slugsOf(all) != "mine" {
		t.Errorf("All() = %s; an archived link should be hidden", slugsOf(all))
	}
	if l, ok, _ := s.Lookup(slug); !ok || l.Target != "/y/a.md" {
		t.Error("an archived link should still work")
	}
	c.advance(days(1))
	if err := s.RecordClick(Click{Slug: slug, Route: "open"}); err != nil {
		t.Fatal(err)
	}
	r := recOf(t, s, slug)
	if r.Archived != nil || r.Clicks != 1 || r.LastClicked == nil {
		t.Fatalf("after a click: %+v", r)
	}
	// A clicked link lasts 180 days after its last click.
	c.advance(days(180) - time.Second)
	if got, _ := s.Archive(); len(got) != 0 {
		t.Fatalf("archived %v before 180 days", got)
	}
	c.advance(time.Second)
	if got, _ := s.Archive(); len(got) != 1 {
		t.Fatalf("not archived at 180 days")
	}
	// Linking the file again restores it and restarts the clock.
	mustEnsure(t, s, "/y/a.md", Meta{})
	if r := recOf(t, s, slug); r.Archived != nil || !r.ArchivesAt().Equal(c.t.Add(days(180))) {
		t.Errorf("after relinking: archived %v, archives at %v", r.Archived, r.ArchivesAt())
	}
	// Your links never archive, and clicks on them don't touch records.
	if err := s.SetArchived("mine", true); err == nil {
		t.Error("archiving one of your links should fail")
	}
}

func TestRemovedSlugsAreNeverGivenToAnotherFile(t *testing.T) {
	s := newTestStore(t, "")
	withClock(s)
	a := mustEnsure(t, s, "/a/readme.md", Meta{})
	if err := s.Delete(a); err != nil {
		t.Fatal(err)
	}
	if b := mustEnsure(t, s, "/b/readme.md", Meta{}); b == a {
		t.Fatalf("a different file got the removed slug %q", a)
	}
	if again := mustEnsure(t, s, "/a/readme.md", Meta{}); again != a {
		t.Errorf("linking the removed file again gave %q, want its old slug %q back", again, a)
	}
	if r := recOf(t, s, a); r.Removed != nil {
		t.Errorf("relinked record is still removed: %+v", r.Removed)
	}
}

func TestRenameLeavesATombstoneThatSendsClicksOn(t *testing.T) {
	s := newTestStore(t, "budget = /y/budget.numbers\n")
	withClock(s)
	slug := mustEnsure(t, s, "/y/a.md", Meta{})
	if err := s.Rename(slug, "plan"); err != nil {
		t.Fatal(err)
	}
	if r := recOf(t, s, slug); r.Removed == nil || r.Removed.Why != "renamed" || r.Removed.To != "plan" {
		t.Fatalf("old slug = %+v", r.Removed)
	}
	if err := s.Rename("plan", slug); err == nil {
		t.Error("renaming onto a tombstone should fail")
	}
	if err := s.Rename("budget", "money"); err != nil {
		t.Fatal(err)
	}
	if r := recOf(t, s, "budget"); r.Removed == nil || r.Removed.To != "money" || r.Origin != "import" {
		t.Errorf("the renamed kept link's old slug = %+v", r)
	}
	if r := recOf(t, s, "money"); !r.Kept || r.Target != "/y/budget.numbers" {
		t.Errorf("money = %+v", r)
	}
}

func TestKeepingALinkKeepsItsRecord(t *testing.T) {
	s := newTestStore(t, "")
	c := withClock(s)
	slug := mustEnsure(t, s, "/y/a.md", Meta{Session: "s1", Note: "why"})
	c.advance(days(40))
	if got, _ := s.Archive(); len(got) != 1 {
		t.Fatalf("archived %v", got)
	}
	if err := s.SetKept(slug, true); err != nil {
		t.Fatal(err)
	}
	r := recOf(t, s, slug)
	if !r.Kept || r.Archived != nil || r.Session != "s1" || r.Note != "why" || !r.Created.Equal(testNow) {
		t.Fatalf("kept = %+v; want it restored with its record", r)
	}
	c.advance(days(400))
	if got, _ := s.Archive(); len(got) != 0 {
		t.Errorf("a kept link archived: %v", got)
	}
	if err := s.SetArchived(slug, true); err == nil {
		t.Error("archiving a kept link should fail")
	}
	// Stop keeping it, and its archive clock starts again from now.
	if err := s.SetKept(slug, false); err != nil {
		t.Fatal(err)
	}
	if r := recOf(t, s, slug); r.Kept || !r.ArchivesAt().Equal(c.t.Add(archiveUnclicked)) {
		t.Errorf("after unkeeping: kept %v, archives at %v", r.Kept, r.ArchivesAt())
	}
	if err := s.SetKept("nope", true); err == nil {
		t.Error("keeping an unknown slug should fail")
	}
}

func TestNotes(t *testing.T) {
	s := newTestStore(t, "a = https://example.com\n")
	if err := s.SetNote("a", "  two\nlines\x1b[0m "+strings.Repeat("x", 300)); err != nil {
		t.Fatal(err)
	}
	if r := recOf(t, s, "a"); strings.ContainsAny(r.Note, "\n\x1b") || len([]rune(r.Note)) != maxNote || !strings.HasPrefix(r.Note, "two lines [0m x") {
		t.Errorf("note = %q", r.Note)
	}
	if err := s.SetNote("a", ""); err != nil || recOf(t, s, "a").Note != "" {
		t.Errorf("clearing the note: %v, %q", err, recOf(t, s, "a").Note)
	}
}

func TestClickCountsWaitAndAreSavedOnFlush(t *testing.T) {
	s := newTestStore(t, "a = https://example.com\n")
	s.FlushDelay = time.Hour
	before := read(t, s.Path)
	for range 3 {
		if err := s.RecordClick(Click{Slug: "a", Route: "redirect", Device: "100.101.102.103"}); err != nil {
			t.Fatal(err)
		}
	}
	if r := recOf(t, s, "a"); r.Clicks != 3 || r.LastDevice != "100.101.102.103" {
		t.Fatalf("in memory: %+v", r)
	}
	if read(t, s.Path) != before {
		t.Error("the file was rewritten on each click")
	}
	if strings.Count(read(t, s.clicksPath()), "\n") != 3 {
		t.Error("the click log should have every click at once")
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if fresh := (&Store{Path: s.Path}); recOf(t, fresh, "a").Clicks != 3 {
		t.Errorf("after Flush the file has %d clicks", recOf(t, fresh, "a").Clicks)
	}
}

func TestUnreadableRecordLinesAreKept(t *testing.T) {
	s := newTestStore(t, "")
	withClock(s)
	good, _ := json.Marshal(Record{Slug: "a", Target: "/a", Created: testNow})
	if err := os.WriteFile(s.Path, []byte(string(good)+"\n{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustEnsure(t, s, "/y/b.md", Meta{})
	if got := read(t, s.Path); !strings.Contains(got, "{not json") || !strings.Contains(got, `"slug":"b-md"`) || !strings.Contains(got, `"slug":"a"`) {
		t.Errorf("records file =\n%s", got)
	}
}

func TestChangesFromAnotherStoreAreSeen(t *testing.T) {
	s1 := newTestStore(t, "")
	s2 := &Store{Path: s1.Path}
	mustEnsure(t, s1, "/y/a.md", Meta{})
	if all, _ := s2.All(); slugsOf(all) != "a-md" {
		t.Fatalf("s2 sees %q", slugsOf(all))
	}
	mustEnsure(t, s2, "/y/b.md", Meta{})
	if all, _ := s1.All(); slugsOf(all) != "a-md,b-md" {
		t.Errorf("s1 sees %q", slugsOf(all))
	}
}

func TestMigrateMovesTheTextFileToRecords(t *testing.T) {
	s := newTestStore(t, "budget = /y/budget.numbers\n")
	withClock(s)
	legacy := filepath.Join(filepath.Dir(s.Path), legacyAutoFile)
	text := "# Links made by sl-link. shortlinker rewrites this file.\na-md = /y/a.md\nb-md = /y/b.md | https://sb/b\nBad Line\n"
	if err := os.WriteFile(legacy, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := s.Migrate("/Users/test")
	if err != nil || m != (Migration{FromText: 2}) {
		t.Fatalf("Migrate = %+v, %v; want 2 from the text file", m, err)
	}
	if got := autoText(t, s); got != "a-md = /y/a.md\nb-md = /y/b.md | https://sb/b\n" {
		t.Errorf("records = %q", got)
	}
	if r := recOf(t, s, "a-md"); !r.Created.Equal(testNow) || r.Origin != "sl-link" {
		t.Errorf("moved record = %+v", r)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("the text file should be renamed")
	}
	if got := read(t, legacy+".before-records"); got != text {
		t.Errorf("backup = %q", got)
	}
	if m, err := s.Migrate("/Users/test"); err != nil || m.Any() {
		t.Errorf("second Migrate moved %+v, %v", m, err)
	}
}

// A `tailscale status --json` with this Mac and one phone.
const fakeStatus = `{"Self":{"HostName":"hub","DNSName":"hub.example.ts.net.","TailscaleIPs":["100.64.0.1"]},
"Peer":{"k1":{"HostName":"localhost","DNSName":"iphone.example.ts.net.","TailscaleIPs":["100.101.102.103"]}}}`

func TestClicksAreLoggedWithDeviceClientRouteAndReferrer(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestStore(t, "")
	withClock(s)
	slug := mustEnsure(t, s, file, Meta{Origin: "sl-link"})
	srv, _ := newTestServerFor(t, s)
	srv.devices = &devices{}
	if err := srv.devices.parse([]byte(fakeStatus)); err != nil {
		t.Fatal(err)
	}
	if rec := send(srv, "GET", "http://hub.example.ts.net/"+slug, "", "User-Agent", iphoneUA,
		"X-Forwarded-For", "100.101.102.103", "Referer", "https://www.notion.so/My-page-123?pvs=4"); rec.Code != 302 {
		t.Fatalf("click = %d", rec.Code)
	}
	send(srv, "GET", "http://hub.example.ts.net/"+slug+"?finder", "", "User-Agent", desktopUA)
	send(srv, "GET", "http://hub.example.ts.net/no-such-link", "", "User-Agent", desktopUA)

	rec := send(srv, "GET", "http://localhost/api/links/"+slug+"/clicks", "")
	var clicks []clickView
	if err := json.Unmarshal(rec.Body.Bytes(), &clicks); err != nil || len(clicks) != 2 {
		t.Fatalf("clicks = %s (%v)", rec.Body, err)
	}
	if c := clicks[1]; c.Device != "iphone" || c.IP != "100.101.102.103" || c.Client != "phone" || c.Route != "serve" || c.Ref != "https://www.notion.so" {
		t.Errorf("phone click = %+v", c)
	}
	if c := clicks[0]; c.Device != "local" || c.Client != "mac" || c.Route != "finder" || c.Ref != "" {
		t.Errorf("Mac click = %+v", c)
	}
	if r := recOf(t, s, slug); r.Clicks != 2 {
		t.Errorf("record counts %d clicks, want 2", r.Clicks)
	}
	all, _ := s.Clicks("", 0)
	if len(all) != 3 || all[0].Route != "unknown" || all[0].Slug != "no-such-link" {
		t.Errorf("click log = %+v", all)
	}
	if strings.Contains(read(t, s.clicksPath()), "My-page") {
		t.Error("the log kept more of the referrer than its origin")
	}
	if fi, err := os.Stat(s.clicksPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("clicks.jsonl mode = %v, %v", fi.Mode().Perm(), err)
	}
}

func TestTombstonesAnswerOldLinks(t *testing.T) {
	s := newTestStore(t, "")
	withClock(s)
	a := mustEnsure(t, s, "/y/<script>.md", Meta{})
	b := mustEnsure(t, s, "/y/b.md", Meta{})
	if err := s.Delete(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(b, "plan"); err != nil {
		t.Fatal(err)
	}
	srv, _ := newTestServerFor(t, s)
	rec := get(srv, "http://hub.example.ts.net/"+a, desktopUA)
	if rec.Code != 410 || !strings.Contains(rec.Body.String(), "/y/&lt;script&gt;.md") {
		t.Errorf("removed link = %d %q", rec.Code, rec.Body)
	}
	rec = get(srv, "http://hub.example.ts.net/"+b+"?finder", desktopUA)
	if rec.Code != 302 || rec.Header().Get("Location") != "/plan?finder" {
		t.Errorf("renamed link = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPublicPortAnswersOnlyToThisHubsName(t *testing.T) {
	srv, _ := newTestServer(t, "a = https://example.com\n")
	for host, want := range map[string]int{"hub.example.ts.net": 302, "HUB.example.ts.net": 302, "other.example.ts.net": 403, "127.0.0.1:7777": 302} {
		if rec := get(srv, "http://"+host+"/a", desktopUA); rec.Code != want {
			t.Errorf("Host %s = %d, want %d", host, rec.Code, want)
		}
	}
	// Tailscale's name for this Mac counts too, and with no name known yet
	// any tailnet name does.
	srv.publicURL = func() string { return "" }
	if rec := get(srv, "http://other.example.ts.net/a", desktopUA); rec.Code != 302 {
		t.Errorf("with no known name, *.ts.net = %d", rec.Code)
	}
	srv.devices = &devices{}
	srv.devices.parse([]byte(fakeStatus))
	if rec := get(srv, "http://hub.example.ts.net/a", desktopUA); rec.Code != 302 {
		t.Errorf("Tailscale's name = %d", rec.Code)
	}
	if rec := get(srv, "http://other.example.ts.net/a", desktopUA); rec.Code != 403 {
		t.Errorf("another name once Tailscale's is known = %d", rec.Code)
	}
}

func TestDetailsStatsAndArchiveAPI(t *testing.T) {
	s := newTestStore(t, "mine = https://example.com\n")
	withClock(s)
	slug := mustEnsure(t, s, "/y/a.md", Meta{Origin: "sl-link", Session: "s1", Note: "why"})
	mustEnsure(t, s, "/y/b.md", Meta{})
	srv, _ := newTestServerFor(t, s)
	get(srv, "http://hub.example.ts.net/mine", desktopUA)

	var l listing
	json.Unmarshal(send(srv, "GET", "http://localhost/api/links?details=1", "").Body.Bytes(), &l)
	byslug := map[string]linkDetails{}
	for _, d := range l.Links {
		byslug[d.Slug] = d
	}
	if d := byslug[slug]; d.Session != "s1" || d.Note != "why" || d.Origin != "sl-link" || d.ArchivesAt == nil || !d.ArchivesAt.Equal(testNow.Add(archiveUnclicked)) {
		t.Errorf("details = %+v", d)
	}
	if d := byslug["mine"]; d.Clicks != 1 || d.LastClicked == nil || !d.Kept || d.LastDevice != "local" || d.ArchivesAt != nil {
		t.Errorf("kept link = %+v", d)
	}

	if rec := send(srv, "POST", "http://localhost/api/links/"+slug+"/archive", `{"archived":true}`); rec.Code != 200 {
		t.Fatalf("archive = %d %s", rec.Code, rec.Body)
	}
	if rec := send(srv, "POST", "http://localhost/api/links/mine/archive", `{"archived":true}`); rec.Code != 400 {
		t.Errorf("archiving yours = %d", rec.Code)
	}
	json.Unmarshal(send(srv, "GET", "http://localhost/api/links?details=1", "").Body.Bytes(), &l)
	if slugsOf2(l.Links) != "mine,b-md" {
		t.Errorf("default listing = %s", slugsOf2(l.Links))
	}
	json.Unmarshal(send(srv, "GET", "http://localhost/api/links?details=1&archived=1", "").Body.Bytes(), &l)
	if slugsOf2(l.Links) != "mine,a-md,b-md" {
		t.Errorf("with archived = %s", slugsOf2(l.Links))
	}

	var st map[string]any
	json.Unmarshal(send(srv, "GET", "http://localhost/api/stats", "").Body.Bytes(), &st)
	for k, want := range map[string]float64{"links": 2, "kept": 1, "archived": 1, "never_clicked": 1, "clicks_week": 1, "made_week": 1} {
		if st[k] != want {
			t.Errorf("stats[%s] = %v, want %v (all: %v)", k, st[k], want, st)
		}
	}
}

func slugsOf2(ds []linkDetails) string {
	var s []string
	for _, d := range ds {
		s = append(s, d.Slug)
	}
	return strings.Join(s, ",")
}

func TestClickLogStaysBounded(t *testing.T) {
	s := newTestStore(t, "a = https://example.com\n")
	withClock(s)
	srv, _ := newTestServerFor(t, s)
	// Slugs nobody could have been given aren't logged, and nor are huge referrers' hosts.
	get(srv, "http://hub.example.ts.net/"+strings.Repeat("x", 5000), desktopUA)
	get(srv, "http://hub.example.ts.net/Not_A_Slug", desktopUA)
	send(srv, "GET", "http://hub.example.ts.net/a", "", "User-Agent", desktopUA, "Referer", "https://"+strings.Repeat("h", 300)+".example/")
	clicks, _ := s.Clicks("", 0)
	if len(clicks) != 1 || clicks[0].Slug != "a" || clicks[0].Ref != "" {
		t.Fatalf("clicks = %+v", clicks)
	}
	// Past the cap the log moves aside, and both files are still read.
	defer func(n int64) { maxClicksFile = n }(maxClicksFile)
	maxClicksFile = 1
	get(srv, "http://hub.example.ts.net/a", desktopUA)
	get(srv, "http://hub.example.ts.net/a", desktopUA)
	if _, err := os.Stat(s.clicksPath() + ".1"); err != nil {
		t.Fatalf("no clicks.jsonl.1: %v", err)
	}
	if clicks, _ := s.Clicks("a", 0); len(clicks) < 2 {
		t.Errorf("after moving aside, %d clicks are readable", len(clicks))
	}
	if strings.Count(read(t, s.clicksPath())+read(t, s.clicksPath()+".1"), "\n") > 2 {
		t.Error("older clicks should be dropped once a second file fills, not kept forever")
	}
}
