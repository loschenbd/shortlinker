package main

import (
	"encoding/json"
	"fmt"
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

func TestOpenRunsOpenOnRegisteredSlug(t *testing.T) {
	srv, opened := newTestServer(t, testLinks)
	rec := get(srv, "http://localhost:7777/o/budget", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if len(*opened) != 1 || (*opened)[0] != "/Users/test/Documents/budget.numbers" {
		t.Errorf("opened = %v, want expanded budget path", *opened)
	}
	if !strings.Contains(rec.Body.String(), "Opened") {
		t.Errorf("body = %q, want done page", rec.Body.String())
	}
}

func TestOpenRejectsForeignHost(t *testing.T) {
	srv, opened := newTestServer(t, testLinks)
	rec := get(srv, "http://evil.example:7777/o/budget", desktopUA)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(*opened) != 0 {
		t.Errorf("opened = %v, want nothing", *opened)
	}
}

func TestOpenUnknownSlug404(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://localhost:7777/o/nope", desktopUA)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestOpenReportsOpenFailure(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	srv.open = func(target string) error { return fmt.Errorf("open failed: no such file") }
	rec := get(srv, "http://localhost:7777/o/budget", desktopUA)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no such file") {
		t.Errorf("body = %q, want open error text", rec.Body.String())
	}
}

func TestResolveEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/api/resolve/budget", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct{ Slug, Target string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Slug != "budget" || body.Target != "~/Documents/budget.numbers" {
		t.Errorf("body = %+v", body)
	}
	if rec := get(srv, "http://hub.ts.example/api/resolve/nope", desktopUA); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", rec.Code)
	}
}

func TestAgentResolvesViaHub(t *testing.T) {
	hubSrv, _ := newTestServer(t, testLinks)
	hub := httptest.NewServer(hubSrv.Handler())
	defer hub.Close()

	agent := NewServer(nil, hub.URL, "7777", "/Users/test")
	opened := &[]string{}
	agent.open = func(target string) error {
		*opened = append(*opened, target)
		return nil
	}
	rec := get(agent, "http://localhost:7777/o/budget", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if len(*opened) != 1 || (*opened)[0] != "/Users/test/Documents/budget.numbers" {
		t.Errorf("opened = %v", *opened)
	}
}

func TestAgentHubUnreachable(t *testing.T) {
	agent := NewServer(nil, "http://127.0.0.1:1", "7777", "/Users/test")
	agent.open = func(target string) error { return nil }
	rec := get(agent, "http://localhost:7777/o/budget", desktopUA)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hub unreachable") {
		t.Errorf("body = %q, want hub-unreachable message", rec.Body.String())
	}
}

func TestAPILinksList(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.ts.example/api/links", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var links map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &links); err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links["daily"] != "obsidian://daily?vault=Main" {
		t.Errorf("links = %v", links)
	}
}

func TestAPILinksAdd(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	body := strings.NewReader(`{"slug":"proj","target":"~/Code/my-app"}`)
	req := httptest.NewRequest("POST", "http://hub.ts.example/api/links", body)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	target, ok, _ := srv.store.Get("proj")
	if !ok || target != "~/Code/my-app" {
		t.Errorf("Get(proj) = %q, %v", target, ok)
	}
}

func TestAPILinksAddInvalid(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	for _, payload := range []string{
		`{"slug":"Bad Slug","target":"/x"}`,
		`{"slug":"api","target":"/x"}`,
		`{"slug":"ok","target":""}`,
		`not json`,
	} {
		req := httptest.NewRequest("POST", "http://hub.ts.example/api/links", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload %q: status = %d, want 400", payload, rec.Code)
		}
	}
}

func TestAPILinksDelete(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	req := httptest.NewRequest("DELETE", "http://hub.ts.example/api/links/budget", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, ok, _ := srv.store.Get("budget"); ok {
		t.Error("budget still present after delete")
	}
	req = httptest.NewRequest("DELETE", "http://hub.ts.example/api/links/nope", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", rec.Code)
	}
}
