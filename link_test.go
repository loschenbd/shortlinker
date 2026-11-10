package main

import (
	"bytes"
	"errors"
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
				return "", nil // sb-link exit 2: not in a SilverBullet space
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
	hub := httptest.NewServer(NewServer(newTestStore(t, testLinks), "", "7778", "/Users/test").LocalHandler())
	defer hub.Close()
	slug, phone, err := postLink(hub.Client(), hub.URL, "~/Documents/notes.md")
	if err != nil || slug != "notes-md" || phone != "" {
		t.Errorf("hub: postLink = %q, %q, %v; want notes-md and no phone page", slug, phone, err)
	}
	if err := postPhone(hub.Client(), hub.URL, "notes-md", "https://hub.example.ts.net:3000/notes"); err != nil {
		t.Fatalf("postPhone: %v", err)
	}
	if _, phone, _ := postLink(hub.Client(), hub.URL, "~/Documents/notes.md"); phone != "https://hub.example.ts.net:3000/notes" {
		t.Errorf("relink: phone = %q, want the saved page", phone)
	}
	if err := postPhone(hub.Client(), hub.URL, "notes-md", "javascript:alert(1)"); err == nil {
		t.Error("postPhone accepted a non-http URL")
	}
	if _, _, err := postLink(hub.Client(), hub.URL, "https://example.com"); err == nil || !strings.Contains(err.Error(), "hub refused") {
		t.Errorf("hub, URL target: err = %v, want hub refused", err)
	}

	agent := httptest.NewServer(NewServer(nil, hub.URL, "7778", "/Users/test").LocalHandler())
	defer agent.Close()
	if _, _, err := postLink(agent.Client(), agent.URL, "~/x.md"); !errors.Is(err, errNoAPI) {
		t.Errorf("agent: err = %v, want errNoAPI", err)
	}

	agent.Close()
	if _, _, err := postLink(http.DefaultClient, agent.URL, "~/x.md"); !errors.Is(err, errUnreachable) {
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
