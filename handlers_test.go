package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testLinks = "budget = ~/Documents/budget.numbers\ndaily = obsidian://daily?vault=Main\n"

const desktopUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
const iphoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"

// newTestServer returns a hub-role server whose open func records targets
// instead of executing /usr/bin/open.
func newTestServer(t *testing.T, content string) (*Server, *[]string) {
	t.Helper()
	srv := NewServer(newTestStore(t, content), "", "7777", "/Users/test")
	opened := &[]string{}
	srv.open = func(target string) error {
		*opened = append(*opened, target)
		return nil
	}
	return srv, opened
}

func get(srv *Server, url, userAgent string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestShortlinkRedirectsSchemeTargets(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/daily", desktopUA)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "obsidian://daily?vault=Main" {
		t.Errorf("Location = %q", loc)
	}
}

func TestShortlinkBouncesFileTargetsToLocalhost(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/budget", desktopUA)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "http://localhost:7777/o/budget" {
		t.Errorf("Location = %q", loc)
	}
}

func TestShortlinkServesMacOnlyPageOnMobile(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/budget", iphoneUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Mac") {
		t.Errorf("body should mention Mac, got %q", rec.Body.String())
	}
}

func TestShortlinkUnknownSlug404(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/nope", desktopUA)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
