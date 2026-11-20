package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTildePath(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/test":             "~",
		"/Users/test/a/b.md":      "~/a/b.md",
		"/Users/tester/b.md":      "/Users/tester/b.md",
		"/private/tmp/scratch.md": "/private/tmp/scratch.md",
	} {
		if got := tildePath(in, "/Users/test"); got != want {
			t.Errorf("tildePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinkPathsPrintsOneLinkPerPath(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "a.pdf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var registered []string
	o := linkOptions{home: home, hubURL: "https://hub.example.ts.net/",
		register: func(target string) (string, string, error) {
			registered = append(registered, target)
			return SlugFromPath(target), "", nil
		}}
	var out, errOut bytes.Buffer
	code := linkPaths([]string{filepath.Join(home, "a.pdf"), "~", filepath.Join(home, "missing.md")}, o, &out, &errOut)
	if code != exitNoSuchPath {
		t.Errorf("code = %d, want %d", code, exitNoSuchPath)
	}
	if want := "https://hub.example.ts.net/a-pdf\nhttps://hub.example.ts.net/file\n"; out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
	if strings.Join(registered, ",") != "~/a.pdf,~" {
		t.Errorf("registered = %v", registered)
	}
	if !strings.Contains(errOut.String(), "missing.md: no such file or folder") {
		t.Errorf("stderr = %q", errOut.String())
	}

	out.Reset()
	o.finder = true
	linkPaths([]string{filepath.Join(home, "a.pdf")}, o, &out, &errOut)
	if out.String() != "https://hub.example.ts.net/a-pdf?finder\n" {
		t.Errorf("--finder: out = %q", out.String())
	}
}

func TestLinkPathsGivesMarkdownASilverBulletPageOnce(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"new.md", "has-page.md", "outside.md", "down.md", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(home, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	saved := map[string]string{}
	o := linkOptions{home: home, hubURL: "https://hub",
		register: func(target string) (string, string, error) {
			if strings.HasSuffix(target, "has-page.md") {
				return "has-page-md", "https://sb/existing", nil
			}
			return SlugFromPath(target), "", nil
		},
		phonePage: func(abs string) (string, error) {
			asked = append(asked, filepath.Base(abs))
			switch filepath.Base(abs) {
			case "outside.md":
				return "", nil // no sb-link on this Mac: no page, nothing to say
			case "down.md":
				return "", errors.New("sb-link: exit status 4")
			}
			return "https://sb/" + filepath.Base(abs), nil
		},
		savePhone: func(slug, url string) error { saved[slug] = url; return nil },
	}
	var out, errOut bytes.Buffer
	paths := []string{"new.md", "has-page.md", "outside.md", "down.md", "notes.txt"}
	for i := range paths {
		paths[i] = filepath.Join(home, paths[i])
	}
	if code := linkPaths(paths, o, &out, &errOut); code != exitOK {
		t.Errorf("code = %d, want 0 (the links work either way)", code)
	}
	if strings.Count(out.String(), "\n") != 5 {
		t.Errorf("out = %q, want 5 links", out.String())
	}
	if strings.Join(asked, ",") != "new.md,outside.md,down.md" {
		t.Errorf("asked sb-link about %v; want only Markdown files with no page yet", asked)
	}
	if len(saved) != 1 || saved["new-md"] != "https://sb/new.md" {
		t.Errorf("saved = %v, want only new-md", saved)
	}
	if !strings.Contains(errOut.String(), "down.md: no SilverBullet page") {
		t.Errorf("stderr = %q, want a note about down.md", errOut.String())
	}
}

// fakeSbLink puts an sb-link first on PATH that prints stdout, writes stderr
// and exits with code, the way the real one reports a page or a refusal.
func fakeSbLink(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{"out.txt": stdout, "err.txt": stderr} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf("#!/bin/sh\ncat \"%[1]s/out.txt\"\ncat \"%[1]s/err.txt\" >&2\nexit %[2]d\n", dir, code)
	if err := os.WriteFile(filepath.Join(dir, "sb-link"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
}

func TestSilverBulletPageSaysWhySbLinkMadeNoPage(t *testing.T) {
	refusal := "sb-link: /p/.drafts/x.md is inside '.drafts/', which SilverBullet doesn't serve\n"
	fakeSbLink(t, 2, "", refusal)
	if url, err := phonePageFor("sb-link")("/p/.drafts/x.md"); url != "" || err == nil || !strings.Contains(err.Error(), "inside '.drafts/'") {
		t.Errorf("exit 2: got %q, %v; want no page and sb-link's reason", url, err)
	}

	fakeSbLink(t, 1, "", "sb-link: login failed: {'status': 'error'}\n")
	if _, err := phonePageFor("sb-link")("/p/x.md"); err == nil || !strings.Contains(err.Error(), "login failed") {
		t.Errorf("exit 1: err = %v; want sb-link's reason", err)
	}

	fakeSbLink(t, 0, "https://sb.example:3000/p/x\n", "")
	if url, err := phonePageFor("sb-link")("/p/x.md"); url != "https://sb.example:3000/p/x" || err != nil {
		t.Errorf("exit 0: got %q, %v", url, err)
	}

	t.Setenv("PATH", t.TempDir()) // no sb-link: this Mac has no SilverBullet
	if url, err := phonePageFor("sb-link")("/p/x.md"); url != "" || err != nil {
		t.Errorf("no sb-link: got %q, %v; want nothing and no note", url, err)
	}
}

// A file linked while sb-link refused it (say, one in a dot-folder it didn't
// serve yet) keeps its link, and linking it again gives that link its
// SilverBullet page, so phones go there instead of plain text.
func TestRelinkGivesAnEarlierLinkItsSilverBulletPage(t *testing.T) {
	home := t.TempDir()
	doc := filepath.Join(home, "Code", "app", ".drafts", "plan.md")
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(newTestStore(t, ""), "", "7778", home)
	srv.auth = newAPIAuth(testToken)
	local := httptest.NewServer(srv.LocalHandler())
	defer local.Close()
	hub := api{base: local.URL, token: testToken, client: local.Client()}
	o := linkOptions{home: home, hubURL: "https://hub.example.ts.net",
		register:  func(target string) (string, string, error) { return postLink(hub, target, Meta{Origin: "sl-link"}) },
		phonePage: phonePageFor("sb-link"),
		savePhone: func(slug, url string) error { return postPhone(hub, slug, url) },
	}

	fakeSbLink(t, 2, "", "sb-link: "+doc+" is inside '.drafts/', which SilverBullet doesn't serve\n")
	var out, errOut bytes.Buffer
	if code := linkPaths([]string{doc}, o, &out, &errOut); code != exitOK {
		t.Errorf("first run: code = %d, want 0 (the link works either way)", code)
	}
	first := out.String()
	if !strings.Contains(errOut.String(), "plan.md: no SilverBullet page, so phones get plain text") ||
		!strings.Contains(errOut.String(), "inside '.drafts/'") {
		t.Errorf("first run: stderr = %q, want the no-page note with sb-link's reason", errOut.String())
	}
	slug := strings.TrimPrefix(strings.TrimSpace(first), "https://hub.example.ts.net/")
	if loc := get(srv, "http://hub.example.ts.net/"+slug, iphoneUA).Header().Get("Location"); loc != "/f/"+slug {
		t.Errorf("first run: phone goes to %q, want /f/%s", loc, slug)
	}

	fakeSbLink(t, 0, "https://hub.example.ts.net:3000/app/plan\n", "")
	out.Reset()
	errOut.Reset()
	linkPaths([]string{doc}, o, &out, &errOut)
	if out.String() != first {
		t.Errorf("relink: printed %q, want the same link %q", out.String(), first)
	}
	if errOut.Len() != 0 {
		t.Errorf("relink: stderr = %q, want nothing", errOut.String())
	}
	if loc := get(srv, "http://hub.example.ts.net/"+slug, iphoneUA).Header().Get("Location"); loc != "https://hub.example.ts.net:3000/app/plan" {
		t.Errorf("relink: phone goes to %q, want the SilverBullet page", loc)
	}
	if loc := get(srv, "http://hub.example.ts.net/"+slug, desktopUA).Header().Get("Location"); loc != "http://localhost:7778/o/"+slug {
		t.Errorf("relink: a Mac goes to %q, want its own opener", loc)
	}
}

func TestLinkPathsReportsUnreachableHub(t *testing.T) {
	o := linkOptions{home: "/Users/test", hubURL: "https://hub",
		register: func(string) (string, string, error) { return "", "", errUnreachable }}
	var out, errOut bytes.Buffer
	if code := linkPaths([]string{t.TempDir()}, o, &out, &errOut); code != exitUnreachable {
		t.Errorf("code = %d, want %d", code, exitUnreachable)
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want nothing", out.String())
	}
}

func TestPostLinkAndPhoneAgainstHubAndAgent(t *testing.T) {
	hubSrv := NewServer(newTestStore(t, testLinks), "", "7778", "/Users/test")
	hubSrv.auth = newAPIAuth(testToken)
	hubHTTP := httptest.NewServer(hubSrv.LocalHandler())
	defer hubHTTP.Close()
	hub := api{base: hubHTTP.URL, token: testToken, client: hubHTTP.Client()}
	slug, phone, err := postLink(hub, "~/Documents/notes.md", Meta{Origin: "sl-link"})
	if err != nil || slug != "notes-md" || phone != "" {
		t.Errorf("hub: postLink = %q, %q, %v; want notes-md and no phone page", slug, phone, err)
	}
	if err := postPhone(hub, "notes-md", "https://hub.example.ts.net:3000/notes"); err != nil {
		t.Fatalf("postPhone: %v", err)
	}
	if _, phone, _ := postLink(hub, "~/Documents/notes.md", Meta{Origin: "sl-link"}); phone != "https://hub.example.ts.net:3000/notes" {
		t.Errorf("relink: phone = %q, want the saved page", phone)
	}
	if err := postPhone(hub, "notes-md", "javascript:alert(1)"); err == nil {
		t.Error("postPhone accepted a non-http URL")
	}
	if _, _, err := postLink(hub, "https://example.com", Meta{Origin: "sl-link"}); err == nil || !strings.Contains(err.Error(), "hub refused") {
		t.Errorf("hub, URL target: err = %v, want hub refused", err)
	}

	if _, _, err := postLink(api{base: hub.base, token: "wrong", client: hub.client}, "~/x.md", Meta{}); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("wrong token: err = %v, want the hub to ask for its token", err)
	}

	agentHTTP := httptest.NewServer(NewServer(nil, hubHTTP.URL, "7778", "/Users/test").LocalHandler())
	defer agentHTTP.Close()
	agent := api{base: agentHTTP.URL, token: testToken, client: agentHTTP.Client()}
	if _, _, err := postLink(agent, "~/x.md", Meta{Origin: "sl-link"}); !errors.Is(err, errNoAPI) {
		t.Errorf("agent: err = %v, want errNoAPI", err)
	}

	agentHTTP.Close()
	if _, _, err := postLink(api{base: agentHTTP.URL, client: http.DefaultClient}, "~/x.md", Meta{}); !errors.Is(err, errUnreachable) {
		t.Errorf("closed: err = %v, want errUnreachable", err)
	}
}

func TestPublicHubURLUsesEnvThenSavedURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHORTLINKER_URL", "https://env.example.ts.net/")
	if u, err := publicHubURL(home); err != nil || u != "https://env.example.ts.net" {
		t.Errorf("env: %q, %v", u, err)
	}
	t.Setenv("SHORTLINKER_URL", "")
	saved := filepath.Join(home, ".config", "shortlinker", "url")
	os.MkdirAll(filepath.Dir(saved), 0o755)
	if err := os.WriteFile(saved, []byte("https://saved.example.ts.net\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// With the saved URL there, tailscale is never run (it takes 10-19 s).
	t.Setenv("PATH", "")
	if u, err := publicHubURL(home); err != nil || u != "https://saved.example.ts.net" {
		t.Errorf("saved: %q, %v", u, err)
	}
}
