package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newServeFixture registers a file slug and a folder slug over a temp tree:
//
//	page.html, site/{a.txt, .env, sub/b.txt, .git/config, out -> ../secret.txt}, secret.txt
func newServeFixture(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("page.html", "<p>caf\u00e9</p>")
	write("secret.txt", "outside")
	write("site/a.txt", "alpha")
	write("site/.env", "TOKEN=x")
	write("site/sub/b.txt", "beta")
	write("site/.git/config", "[core]")
	if err := os.Symlink(filepath.Join(dir, "secret.txt"), filepath.Join(dir, "site", "out")); err != nil {
		t.Fatal(err)
	}
	links := "page = " + filepath.Join(dir, "page.html") + "\n" +
		"site = " + filepath.Join(dir, "site") + "\n" +
		"daily = obsidian://daily?vault=Main\n"
	srv, _ := newTestServer(t, links)
	return srv, dir
}

const tailnetHost = "http://hub.example.ts.net"

func TestServeFile(t *testing.T) {
	srv, _ := newServeFixture(t)
	rec := get(srv, tailnetHost+"/f/page", iphoneUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if rec.Body.String() != "<p>caf\u00e9</p>" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
}

func TestServeFileHasNoSubpaths(t *testing.T) {
	srv, _ := newServeFixture(t)
	if rec := get(srv, tailnetHost+"/f/page/x", iphoneUA); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestServeFolderRedirectsToTrailingSlash(t *testing.T) {
	srv, _ := newServeFixture(t)
	rec := get(srv, tailnetHost+"/f/site", iphoneUA)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/f/site/" {
		t.Fatalf("status = %d, Location = %q; want 302 /f/site/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestServeFolderListingHidesDotfiles(t *testing.T) {
	srv, _ := newServeFixture(t)
	rec := get(srv, tailnetHost+"/f/site/", iphoneUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`href="a.txt"`, `href="sub/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("listing missing %s:\n%s", want, body)
		}
	}
	for _, hidden := range []string{".env", ".git"} {
		if strings.Contains(body, hidden) {
			t.Errorf("listing shows %s:\n%s", hidden, body)
		}
	}
}

func TestServeFolderFiles(t *testing.T) {
	srv, _ := newServeFixture(t)
	for path, want := range map[string]string{"/f/site/a.txt": "alpha", "/f/site/sub/b.txt": "beta"} {
		rec := get(srv, tailnetHost+path, iphoneUA)
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s: status = %d, body = %q; want 200 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestServeFolderServesIndexHTML(t *testing.T) {
	srv, dir := newServeFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "site", "sub", "index.html"), []byte("<h1>home</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := get(srv, tailnetHost+"/f/site/sub/", iphoneUA)
	if rec.Code != http.StatusOK || rec.Body.String() != "<h1>home</h1>" {
		t.Errorf("status = %d, body = %q; want the index.html", rec.Code, rec.Body.String())
	}
}

func TestServeFolderRefusesDotPathsAndEscapes(t *testing.T) {
	srv, _ := newServeFixture(t)
	for _, path := range []string{
		"/f/site/.env",
		"/f/site/.git/config",
		"/f/site/out",               // symlink to a file outside the folder
		"/f/site/%2e%2e/secret.txt", // encoded .. survives path cleaning
	} {
		rec := get(srv, tailnetHost+path, iphoneUA)
		if rec.Code == http.StatusOK {
			t.Errorf("%s: status 200, body %q; want it refused", path, rec.Body.String())
		}
	}
}

func TestServeRejectsForeignHost(t *testing.T) {
	srv, _ := newServeFixture(t)
	// A DNS-rebinding page reaches 127.0.0.1:7777 under its own hostname.
	if rec := get(srv, "http://attacker.example:7777/f/page", desktopUA); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	for _, host := range []string{"http://localhost:7777", "http://127.0.0.1:7777"} {
		if rec := get(srv, host+"/f/page", desktopUA); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", host, rec.Code)
		}
	}
}

func TestServeRedirectSlug404(t *testing.T) {
	srv, _ := newServeFixture(t)
	if rec := get(srv, tailnetHost+"/f/daily", iphoneUA); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestShortlinkSendsMobileToServedFile(t *testing.T) {
	srv, _ := newServeFixture(t)
	rec := get(srv, tailnetHost+"/page", iphoneUA)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/f/page" {
		t.Errorf("status = %d, Location = %q; want 302 /f/page", rec.Code, rec.Header().Get("Location"))
	}
}

func TestServeScriptsAsPlainText(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"app.ts", "run.sh"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("echo hi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv, _ := newTestServer(t, "app = "+filepath.Join(dir, "app.ts")+"\nrun = "+filepath.Join(dir, "run.sh")+"\n")
	for _, slug := range []string{"app", "run"} {
		rec := get(srv, tailnetHost+"/f/"+slug, iphoneUA)
		if ct := rec.Header().Get("Content-Type"); rec.Code != http.StatusOK || ct != "text/plain; charset=utf-8" {
			t.Errorf("%s: status = %d, Content-Type = %q", slug, rec.Code, ct)
		}
	}
}

func TestServeSandboxesScriptablePagesUnlessLinkedByHand(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"hand.html":       "<p>mine</p>",
		"auto.html":       "<p>sl-link</p>",
		"auto.pdf":        "%PDF-1.4",
		"auto.svg":        `<svg xmlns="http://www.w3.org/2000/svg"/>`,
		"site/saved.html": "<p>saved page</p>",
		"noext":           "<!doctype html><p>sniffed</p>",
	} {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hand := "hand = " + filepath.Join(dir, "hand.html") + "\n" +
		"site = " + filepath.Join(dir, "site") + "\n"
	auto := "auto-html = " + filepath.Join(dir, "auto.html") + "\n" +
		"auto-pdf = " + filepath.Join(dir, "auto.pdf") + "\n" +
		"auto-svg = " + filepath.Join(dir, "auto.svg") + "\n" +
		"noext = " + filepath.Join(dir, "noext") + "\n"
	srv, _ := newTestServerFor(t, newSplitStore(t, hand, auto))
	for path, wantSandbox := range map[string]bool{
		"/f/hand":            false, // a page linked by hand keeps its storage
		"/f/auto-html":       true,
		"/f/auto-svg":        true,
		"/f/noext":           true,  // sniffed as HTML
		"/f/auto-pdf":        false, // can't run script here
		"/f/site/saved.html": true,  // inside a folder link
		"/f/site/":           true,  // the listing
	} {
		rec := get(srv, tailnetHost+path, iphoneUA)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d", path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Security-Policy") == sandboxPolicy; got != wantSandbox {
			t.Errorf("%s (%s): sandboxed = %v, want %v", path, rec.Header().Get("Content-Type"), got, wantSandbox)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff header", path)
		}
	}
}
