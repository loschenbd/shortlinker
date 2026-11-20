package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Anyone can make /tmp/.git; a pipe there used to hang every link under it,
// with the store locked.
func TestProjectOfIgnoresAPlantedGitPipe(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, ".git"), 0o644); err != nil {
		t.Skip("can't make a FIFO here:", err)
	}
	done := make(chan string, 1)
	go func() { done <- projectOf(filepath.Join(dir, "notes.md"), testHome) }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("project = %q, want none", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("projectOf blocked on a FIFO named .git")
	}
	big := t.TempDir()
	os.WriteFile(filepath.Join(big, ".git"), []byte("gitdir: "+strings.Repeat("/x", 1<<20)), 0o644)
	projectOf(filepath.Join(big, "a.md"), testHome) // reads at most maxGitFile; just mustn't fail or hang
}

// Something else listening on the API port (another account's server,
// while the hub is down) must never be sent the token.
func TestCommandsCheckTheHubBeforeSendingTheToken(t *testing.T) {
	var sawToken bool
	impostor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), testToken) {
			sawToken = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"proof":"0000","code":"` + strings.Repeat("a", 32) + `","slug":"x"}`))
	}))
	defer impostor.Close()
	a := api{base: impostor.URL, token: testToken, client: impostor.Client(), proof: &proofOnce{}}
	if _, _, err := postLink(a, "~/x.md", Meta{}); !errors.Is(err, errNotHub) {
		t.Errorf("postLink to an impostor: %v, want errNotHub", err)
	}
	if err := a.do(http.MethodPost, "/api/manage-code", struct{}{}, nil); !errors.Is(err, errNotHub) {
		t.Errorf("manage-code from an impostor: %v", err)
	}
	if sawToken {
		t.Error("the impostor was sent the token")
	}

	srv, _ := newTestServer(t, "")
	hub := httptest.NewServer(srv.LocalHandler())
	defer hub.Close()
	real := api{base: hub.URL, token: testToken, client: hub.Client(), proof: &proofOnce{}}
	if _, _, err := postLink(real, "~/x.md", Meta{}); err != nil {
		t.Errorf("postLink to the real hub: %v", err)
	}
	for _, n := range []string{"", "xyz", strings.Repeat("a", 31), strings.Repeat("a", 65)} {
		if rec := send(srv, "GET", "http://127.0.0.1:7778/api/hub-proof?n="+n, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("hub-proof with n=%q: %d", n, rec.Code)
		}
	}
}

func TestManagePageURLIsBuiltHere(t *testing.T) {
	code := strings.Repeat("ab", 16)
	if got, err := managePageURL("http://127.0.0.1:7778", code); err != nil || got != "http://localhost:7778/manage#code="+code {
		t.Errorf("managePageURL = %q, %v", got, err)
	}
	for _, c := range [][2]string{{"http://127.0.0.1:7778", "not hex"}, {"http://127.0.0.1:7778", "abcd"}, {"http://127.0.0.1", code}, {"%zz", code}} {
		if _, err := managePageURL(c[0], c[1]); err == nil {
			t.Errorf("managePageURL(%q, %q): want an error", c[0], c[1])
		}
	}
	if got := appleScriptString(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("appleScriptString = %s", got)
	}
}

// A page that fetches /o/ in the background (an <img>, fetch) mustn't open
// files; a click, which is a navigation, does.
func TestOpenNeedsANavigation(t *testing.T) {
	srv, opened := newTestServer(t, "doc = /Users/test/doc.pdf\n")
	for dest, want := range map[string]int{"image": 403, "empty": 403, "iframe": 403, "document": 200, "": 200} {
		*opened = nil
		rec := send(srv, "GET", "http://localhost:7778/o/doc", "", "User-Agent", desktopUA, "Sec-Fetch-Dest", dest)
		if rec.Code != want || (want == 403) != (len(*opened) == 0) {
			t.Errorf("Sec-Fetch-Dest %q: %d, opened %v; want %d", dest, rec.Code, *opened, want)
		}
	}
}

func TestOpenJudgesASymlinkByWhatItPointsTo(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "Calculator.app")
	os.MkdirAll(app, 0o755)
	guide := filepath.Join(dir, "guide.pdf")
	if err := os.Symlink(app, guide); err != nil {
		t.Fatal(err)
	}
	srv, opened := newTestServer(t, "guide = "+guide+"\n")
	get(srv, "http://localhost:7778/o/guide", desktopUA)
	if strings.Join(*opened, ",") != "reveal "+guide {
		t.Errorf("opened = %v; want it shown in Finder, not run", *opened)
	}
}

func TestServeRefusesSymlinksOutOfTheirFolder(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	repo := filepath.Join(root, "repo")
	os.MkdirAll(repo, 0o755)
	os.WriteFile(secret, []byte("the key"), 0o600)
	os.WriteFile(filepath.Join(repo, "real.txt"), []byte("fine"), 0o644)
	os.Symlink("../secret", filepath.Join(repo, "notes.txt"))
	os.Symlink("real.txt", filepath.Join(repo, "alias.txt"))
	srv, _ := newTestServer(t, "notes-txt = "+filepath.Join(repo, "notes.txt")+"\nalias-txt = "+filepath.Join(repo, "alias.txt")+"\n")
	if rec := get(srv, tailnetHost+"/f/notes-txt", iphoneUA); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "the key") {
		t.Errorf("symlink out of its folder: %d %q", rec.Code, rec.Body)
	}
	if rec := get(srv, tailnetHost+"/f/alias-txt", iphoneUA); rec.Code != http.StatusOK || rec.Body.String() != "fine" {
		t.Errorf("symlink within its folder: %d %q", rec.Code, rec.Body)
	}
}

func TestPrintableEscapesControlCharacters(t *testing.T) {
	for in, want := range map[string]string{"plain é.md": "plain é.md", "a\x1b]0;owned\x07b": `a\x1b]0;owned\x07b`, "tab\there": `tab\x09here`} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}

// Whatever gets linked, /f/ never serves shortlinker's own folder: the
// token, the records and the click log live there.
func TestServeNeverHandsOutShortlinkersOwnFolder(t *testing.T) {
	home := t.TempDir()
	cfg := configDir(home)
	backup := filepath.Join(cfg, "backup-1")
	os.MkdirAll(backup, 0o700)
	os.WriteFile(filepath.Join(cfg, "token"), []byte("secret-token"), 0o600)
	os.WriteFile(filepath.Join(backup, "links.txt"), []byte("private = ~/x\n"), 0o600)
	os.WriteFile(filepath.Join(home, "fine.txt"), []byte("fine"), 0o644)
	dotconfig := filepath.Join(home, ".config")
	srv, opened := newTestServer(t, "cfg = "+cfg+"\nbackup = "+backup+"\ntoken = "+filepath.Join(cfg, "token")+
		"\ndotconfig = "+dotconfig+"\nfine = "+filepath.Join(home, "fine.txt")+"\n")
	srv.home = home
	for _, path := range []string{"/f/cfg/", "/f/cfg/token", "/f/backup/", "/f/backup/links.txt", "/f/token", "/f/dotconfig/shortlinker/token", "/f/dotconfig/shortlinker/"} {
		rec := get(srv, tailnetHost+path, iphoneUA)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "private") {
			t.Errorf("%s: %d %q; want 404", path, rec.Code, rec.Body)
		}
	}
	if rec := get(srv, tailnetHost+"/f/fine", iphoneUA); rec.Code != http.StatusOK {
		t.Errorf("an ordinary file: %d", rec.Code)
	}
	if rec := get(srv, "http://localhost:7778/o/backup", desktopUA); rec.Code != http.StatusOK || len(*opened) != 1 {
		t.Errorf("opening the backup on this Mac: %d, opened %v", rec.Code, *opened)
	}
}

// The manage page's URL holds a sign-in code, and every account can read
// every process's arguments, so the URL goes to osascript on stdin.
func TestOpenURLKeepsTheCodeOutOfArguments(t *testing.T) {
	u := "http://localhost:7778/manage#code=0123456789abcdef0123456789abcdef"
	cmd := openURLCommand(u)
	for _, a := range cmd.Args {
		if strings.Contains(a, "code=") {
			t.Fatalf("the code is in the arguments: %q", cmd.Args)
		}
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if want := "open location " + appleScriptString(u); !strings.Contains(string(in), want) {
		t.Errorf("stdin = %q; want %q", in, want)
	}
}

type fakeInfo struct {
	os.FileInfo
	uid uint32
}

func (f fakeInfo) Sys() any { return &syscall.Stat_t{Uid: f.uid} }

// A .git someone else made (in /tmp, say) says nothing about your files.
func TestOnlyMyOwnGitCounts(t *testing.T) {
	me := uint32(os.Getuid())
	if !ownedByMe(fakeInfo{uid: me}) {
		t.Error("my own .git doesn't count")
	}
	if ownedByMe(fakeInfo{uid: me + 1}) {
		t.Error("another account's .git counts")
	}
}
