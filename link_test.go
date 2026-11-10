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
	if err := os.WriteFile(filepath.Join(home, "a.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var registered []string
	register := func(target string) (string, error) {
		registered = append(registered, target)
		return SlugFromPath(target), nil
	}
	var out, errOut bytes.Buffer
	code := linkPaths([]string{filepath.Join(home, "a.md"), "~", filepath.Join(home, "missing.md")},
		home, "https://hub.example.ts.net/", register, &out, &errOut)
	if code != exitNoSuchPath {
		t.Errorf("code = %d, want %d", code, exitNoSuchPath)
	}
	if want := "https://hub.example.ts.net/a-md\nhttps://hub.example.ts.net/file\n"; out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
	if strings.Join(registered, ",") != "~/a.md,~" {
		t.Errorf("registered = %v", registered)
	}
	if !strings.Contains(errOut.String(), "missing.md: no such file or folder") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestLinkPathsReportsUnreachableHub(t *testing.T) {
	register := func(string) (string, error) { return "", errUnreachable }
	var out, errOut bytes.Buffer
	if code := linkPaths([]string{t.TempDir()}, "/Users/test", "https://hub", register, &out, &errOut); code != exitUnreachable {
		t.Errorf("code = %d, want %d", code, exitUnreachable)
	}
	if out.Len() != 0 {
		t.Errorf("out = %q, want nothing", out.String())
	}
}

func TestPostLinkAgainstHubAndAgent(t *testing.T) {
	hub := httptest.NewServer(NewServer(newTestStore(t, testLinks), "", "7778", "/Users/test").LocalHandler())
	defer hub.Close()
	slug, err := postLink(hub.Client(), hub.URL, "~/Documents/report.pdf")
	if err != nil || slug != "report-pdf" {
		t.Errorf("hub: postLink = %q, %v; want report-pdf", slug, err)
	}
	if _, err := postLink(hub.Client(), hub.URL, "https://example.com"); err == nil || !strings.Contains(err.Error(), "hub refused") {
		t.Errorf("hub, URL target: err = %v, want hub refused", err)
	}

	agent := httptest.NewServer(NewServer(nil, hub.URL, "7778", "/Users/test").LocalHandler())
	defer agent.Close()
	if _, err := postLink(agent.Client(), agent.URL, "~/x.md"); !errors.Is(err, errNoAPI) {
		t.Errorf("agent: err = %v, want errNoAPI", err)
	}

	agent.Close()
	if _, err := postLink(http.DefaultClient, agent.URL, "~/x.md"); !errors.Is(err, errUnreachable) {
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
