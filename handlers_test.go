package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testLinks = "budget = ~/Documents/budget.numbers\ndaily = obsidian://daily?vault=Main\n"

// testToken is the hub token test servers use; get and send pass it.
const testToken = "test-token"

const desktopUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
const iphoneUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"

// newTestServer returns a hub-role server whose open func records targets
// instead of executing /usr/bin/open.
func newTestServer(t *testing.T, content string) (*Server, *[]string) {
	t.Helper()
	return newTestServerFor(t, newTestStore(t, content))
}

func newTestServerFor(t *testing.T, store *Store) (*Server, *[]string) {
	t.Helper()
	srv := NewServer(store, "", "7778", "/Users/test")
	srv.auth = newAPIAuth(testToken)
	srv.publicURL = func() string { return "https://hub.example.ts.net" }
	opened := &[]string{}
	srv.open = func(target string) error {
		*opened = append(*opened, target)
		return nil
	}
	srv.reveal = func(target string) error {
		*opened = append(*opened, "reveal "+target)
		return nil
	}
	return srv, opened
}

func get(srv *Server, url, userAgent string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	route(srv, req).ServeHTTP(rec, req)
	return rec
}

// send makes an API call with the token and, if body isn't "", a JSON body.
// headers are name, value pairs that override those.
func send(srv *Server, method, url, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	rec := httptest.NewRecorder()
	route(srv, req).ServeHTTP(rec, req)
	return rec
}

// route picks the port a request would arrive on in production: /o/ and the
// link-changing API live on the local port, everything else on the public
// one. TestPublicPortHasNoLocalRoutes checks the split itself.
func route(srv *Server, req *http.Request) http.Handler {
	p := req.URL.Path
	if strings.HasPrefix(p, "/o/") || strings.HasPrefix(p, "/api/links") || p == "/manage" ||
		p == "/api/prune" || p == "/api/manage-code" || p == "/api/session" {
		return srv.LocalHandler()
	}
	return srv.PublicHandler()
}

func TestShortlinkRedirectsSchemeTargets(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.example.ts.net/daily", desktopUA)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "obsidian://daily?vault=Main" {
		t.Errorf("Location = %q", loc)
	}
}

func TestShortlinkBouncesFileTargetsToLocalhost(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.example.ts.net/budget", desktopUA)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "http://localhost:7778/o/budget" {
		t.Errorf("Location = %q", loc)
	}
}

func TestShortlinkSendsMobileFileTargetsToServe(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.example.ts.net/budget", iphoneUA)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/f/budget" {
		t.Errorf("Location = %q, want /f/budget", loc)
	}
}

func TestShortlinkUnknownSlug404(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.example.ts.net/nope", desktopUA)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestOpenRunsOpenOnRegisteredSlug(t *testing.T) {
	srv, opened := newTestServer(t, testLinks)
	rec := get(srv, "http://localhost:7778/o/budget", desktopUA)
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
	rec := get(srv, "http://evil.example:7778/o/budget", desktopUA)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(*opened) != 0 {
		t.Errorf("opened = %v, want nothing", *opened)
	}
}

func TestOpenUnknownSlug404(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://localhost:7778/o/nope", desktopUA)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestOpenReportsOpenFailure(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	srv.open = func(target string) error { return fmt.Errorf("open failed: no such file") }
	rec := get(srv, "http://localhost:7778/o/budget", desktopUA)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no such file") {
		t.Errorf("body = %q, want open error text", rec.Body.String())
	}
}

func TestResolveEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.example.ts.net/api/resolve/budget", desktopUA)
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
	if rec := get(srv, "http://hub.example.ts.net/api/resolve/nope", desktopUA); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", rec.Code)
	}
}

func TestAgentResolvesViaHub(t *testing.T) {
	hubSrv, _ := newTestServer(t, testLinks)
	hub := httptest.NewServer(hubSrv.PublicHandler())
	defer hub.Close()

	agent := NewServer(nil, hub.URL, "7778", "/Users/test")
	opened := &[]string{}
	agent.open = func(target string) error {
		*opened = append(*opened, target)
		return nil
	}
	rec := get(agent, "http://localhost:7778/o/budget", desktopUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if len(*opened) != 1 || (*opened)[0] != "/Users/test/Documents/budget.numbers" {
		t.Errorf("opened = %v", *opened)
	}
}

func TestAgentHubUnreachable(t *testing.T) {
	agent := NewServer(nil, "http://127.0.0.1:1", "7778", "/Users/test")
	agent.open = func(target string) error { return nil }
	rec := get(agent, "http://localhost:7778/o/budget", desktopUA)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hub unreachable") {
		t.Errorf("body = %q, want hub-unreachable message", rec.Body.String())
	}
}

func TestAPILinksList(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := get(srv, "http://127.0.0.1:7778/api/links", desktopUA)
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

func TestAPILinksAddRejectsNonJSONContentType(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := send(srv, "POST", "http://127.0.0.1:7778/api/links", `{"slug":"proj","target":"~/Code/my-app"}`, "Content-Type", "text/plain")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if _, ok, _ := srv.store.Get("proj"); ok {
		t.Error("link was added despite wrong Content-Type")
	}
	var result map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["error"] != "Content-Type must be application/json" {
		t.Errorf("error = %q, want %q", result["error"], "Content-Type must be application/json")
	}
}

func TestAPILinksAdd(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := send(srv, "POST", "http://127.0.0.1:7778/api/links", `{"slug":"proj","target":"~/Code/my-app"}`)
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
		rec := send(srv, "POST", "http://127.0.0.1:7778/api/links", payload)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("payload %q: status = %d, want 400", payload, rec.Code)
		}
	}
}

func TestAPILinksDelete(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	rec := send(srv, "DELETE", "http://127.0.0.1:7778/api/links/budget", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if _, ok, _ := srv.store.Get("budget"); ok {
		t.Error("budget still present after delete")
	}
	rec = send(srv, "DELETE", "http://127.0.0.1:7778/api/links/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug status = %d, want 404", rec.Code)
	}
}

func TestAPILinksAddWithoutSlugPicksOne(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	post := func(payload string) (int, map[string]any) {
		rec := send(srv, "POST", "http://127.0.0.1:7778/api/links", payload)
		var result map[string]any
		json.Unmarshal(rec.Body.Bytes(), &result)
		return rec.Code, result
	}
	code, result := post(`{"target":"~/Documents/Plan Notes.md"}`)
	if code != http.StatusOK || result["slug"] != "plan-notes-md" || result["created"] != true {
		t.Fatalf("first: status = %d, result = %v", code, result)
	}
	code, result = post(`{"target":"/Users/test/Documents/Plan Notes.md"}`)
	if code != http.StatusOK || result["slug"] != "plan-notes-md" || result["created"] != false {
		t.Errorf("same file again: status = %d, result = %v", code, result)
	}
	code, result = post(`{"target":"/Users/test/Documents/budget.numbers"}`)
	if code != http.StatusOK || result["slug"] != "budget" {
		t.Errorf("hand-made link: status = %d, result = %v; want budget", code, result)
	}
	if code, _ = post(`{"target":"https://example.com"}`); code != http.StatusBadRequest {
		t.Errorf("URL without slug: status = %d, want 400", code)
	}
}

func TestPublicPortHasNoLocalRoutes(t *testing.T) {
	srv, opened := newTestServer(t, testLinks)
	pub := srv.PublicHandler()
	// A tailnet device can send any Host header through tailscale serve.
	for _, c := range []struct{ method, url, body string }{
		{"GET", "http://localhost:7777/o/budget", ""},
		{"GET", "http://localhost:7777/api/links", ""},
		{"POST", "http://localhost:7777/api/links", `{"slug":"x","target":"/etc/hosts"}`},
		{"POST", "http://localhost:7777/api/links", `{"target":"/etc/hosts"}`},
		{"DELETE", "http://localhost:7777/api/links/budget", ""},
		{"POST", "http://localhost:7777/api/prune", `{}`},
	} {
		req := httptest.NewRequest(c.method, c.url, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		pub.ServeHTTP(rec, req)
		if rec.Code < 400 {
			t.Errorf("%s %s on the public port: status = %d, want 4xx", c.method, c.url, rec.Code)
		}
	}
	if len(*opened) != 0 {
		t.Errorf("opened %v from the public port", *opened)
	}
	if links, _ := srv.store.All(); len(links) != 2 {
		t.Errorf("links changed from the public port: %v", links)
	}
	// A DNS-rebinding page in a browser on the hub can't even ask what a
	// slug points to.
	for _, url := range []string{"http://rebind.example:7777/api/resolve/budget", "http://rebind.example:7777/budget"} {
		if rec := get(srv, url, desktopUA); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: status = %d, want 403", url, rec.Code)
		}
	}
}

func TestLocalPortRefusesForeignHost(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	for _, url := range []string{"http://rebind.example:7778/api/links", "http://hub.example.ts.net:7778/api/links"} {
		if rec := get(srv, url, desktopUA); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: status = %d, want 403", url, rec.Code)
		}
	}
	if rec := get(srv, "http://127.0.0.1:7778/api/links", desktopUA); rec.Code != http.StatusOK {
		t.Errorf("GET from 127.0.0.1: status = %d, want 200", rec.Code)
	}
}

func TestOpenShowsAppsAndScriptsInFinder(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	doc := filepath.Join(dir, "notes.pdf")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	links := "app = /Applications/Calculator.app\nrun = ~/bin/deploy.command\nsh = ~/x/setup.sh\nexe = " + exe + "\ndoc = " + doc + "\ndir = " + dir + "\n"
	srv, opened := newTestServer(t, links)
	for _, slug := range []string{"app", "run", "sh", "exe", "doc", "dir"} {
		rec := get(srv, "http://localhost:7778/o/"+slug, desktopUA)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d", slug, rec.Code)
		}
	}
	want := []string{
		"reveal /Applications/Calculator.app",
		"reveal /Users/test/bin/deploy.command",
		"reveal /Users/test/x/setup.sh",
		"reveal " + exe,
		doc,
		dir,
	}
	if strings.Join(*opened, "\n") != strings.Join(want, "\n") {
		t.Errorf("opened = %q\nwant      %q", *opened, want)
	}
	if rec := get(srv, "http://localhost:7778/o/app", desktopUA); !strings.Contains(rec.Body.String(), "Shown in Finder") {
		t.Errorf("app page = %q, want the Shown in Finder page", rec.Body.String())
	}
}

func TestAPIPrune(t *testing.T) {
	dir := t.TempDir()
	kept := filepath.Join(dir, "kept.md")
	if err := os.WriteFile(kept, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := newTestServerFor(t, newSplitStore(t, "gone-by-hand = /nope/hand.md\n", "kept-md = "+kept+"\ngone-md = /nope/gone.md\n"))
	prune := func(body string) (int, string) {
		rec := send(srv, "POST", "http://127.0.0.1:7778/api/prune", body)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	if code, body := prune(`{"dry_run":true}`); code != http.StatusOK || body != `{"dry_run":true,"removed":["gone-md"]}` {
		t.Errorf("dry run: %d %s", code, body)
	}
	if _, ok, _ := srv.store.Get("gone-md"); !ok {
		t.Error("dry run removed gone-md")
	}
	if code, body := prune(`{}`); code != http.StatusOK || body != `{"dry_run":false,"removed":["gone-md"]}` {
		t.Errorf("prune: %d %s", code, body)
	}
	if links, _ := srv.store.All(); slugsOf(links) != "gone-by-hand,kept-md" {
		t.Errorf("after prune: %v, want gone-by-hand and kept-md", links)
	}
}

func TestShortlinkSendsPhonesToPhonePage(t *testing.T) {
	srv, _ := newTestServer(t, "notes = ~/Documents/notes.md | https://hub.ts.net:3000/notes\nplain = ~/Documents/plain.md\n")
	for _, c := range []struct{ slug, ua, want string }{
		{"notes", iphoneUA, "https://hub.ts.net:3000/notes"},
		{"plain", iphoneUA, "/f/plain"},
		{"notes", desktopUA, "http://localhost:7778/o/notes"},
	} {
		rec := get(srv, "http://hub.example.ts.net/"+c.slug, c.ua)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != c.want {
			t.Errorf("%s: %d -> %q, want 302 -> %q", c.slug, rec.Code, rec.Header().Get("Location"), c.want)
		}
	}
}

func TestFinderLinkShowsFileInFinder(t *testing.T) {
	srv, opened := newTestServer(t, testLinks)
	rec := get(srv, "http://hub.example.ts.net/budget?finder", desktopUA)
	if loc := rec.Header().Get("Location"); loc != "http://localhost:7778/o/budget?reveal=1" {
		t.Errorf("Mac: Location = %q", loc)
	}
	if loc := get(srv, "http://hub.example.ts.net/budget?finder", iphoneUA).Header().Get("Location"); loc != "/f/budget" {
		t.Errorf("phone: Location = %q, want /f/budget", loc)
	}
	rec = get(srv, "http://localhost:7778/o/budget?reveal=1", desktopUA)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Shown in Finder") {
		t.Errorf("/o/?reveal=1: %d %q", rec.Code, rec.Body.String())
	}
	if strings.Join(*opened, ",") != "reveal /Users/test/Documents/budget.numbers" {
		t.Errorf("opened = %v", *opened)
	}
}

func TestAPINeedsTheToken(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	for _, c := range []struct {
		name, auth string
		want       int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"token", "Bearer " + testToken, http.StatusOK},
	} {
		for _, call := range []struct{ method, url, body string }{
			{"GET", "http://127.0.0.1:7778/api/links", ""},
			{"POST", "http://127.0.0.1:7778/api/links", `{"target":"/etc/hosts"}`},
			{"POST", "http://127.0.0.1:7778/api/prune", `{"dry_run":true}`},
		} {
			rec := send(srv, call.method, call.url, call.body, "Authorization", c.auth)
			if rec.Code != c.want {
				t.Errorf("%s: %s %s = %d, want %d", c.name, call.method, call.url, rec.Code, c.want)
			}
		}
	}
	// Without the token nothing was added: only the call with it did.
	if links, _ := srv.store.All(); slugsOf(links) != "budget,daily,hosts" {
		t.Errorf("links = %s", slugsOf(links))
	}
	// A hub that loaded no token refuses everything.
	srv.auth = nil
	if rec := send(srv, "GET", "http://127.0.0.1:7778/api/links", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token loaded: status = %d, want 401", rec.Code)
	}
}

func TestAPIRefusesOtherSitesEvenWithTheToken(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	for _, site := range []string{"cross-site", "same-site"} {
		if rec := send(srv, "DELETE", "http://localhost:7778/api/links/budget", "", "Sec-Fetch-Site", site); rec.Code != http.StatusForbidden {
			t.Errorf("DELETE from a %s page: status = %d, want 403", site, rec.Code)
		}
		if rec := send(srv, "POST", "http://localhost:7778/api/session", `{"code":"x"}`, "Sec-Fetch-Site", site); rec.Code != http.StatusForbidden {
			t.Errorf("session from a %s page: status = %d, want 403", site, rec.Code)
		}
	}
	if _, ok, _ := srv.store.Get("budget"); !ok {
		t.Error("budget was deleted by a web page")
	}
}

func TestManagePageSignsInWithAOneTimeCode(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	page := get(srv, "http://localhost:7778/manage", desktopUA)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "<title>shortlinker</title>") || strings.Contains(page.Body.String(), testToken) {
		t.Fatalf("manage page: %d, has the token: %v", page.Code, strings.Contains(page.Body.String(), testToken))
	}
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}

	var code struct{ Code, URL string }
	json.Unmarshal(send(srv, "POST", "http://127.0.0.1:7778/api/manage-code", "{}").Body.Bytes(), &code)
	if code.Code == "" || code.URL != "http://localhost:7778/manage#code="+code.Code {
		t.Fatalf("code = %+v", code)
	}
	trade := func(c string) *httptest.ResponseRecorder {
		return send(srv, "POST", "http://localhost:7778/api/session", `{"code":"`+c+`"}`, "Authorization", "", "Sec-Fetch-Site", "same-origin")
	}
	rec := trade(code.Code)
	var sess struct{ Session string }
	json.Unmarshal(rec.Body.Bytes(), &sess)
	if rec.Code != http.StatusOK || sess.Session == "" {
		t.Fatalf("trading the code: %d %s", rec.Code, rec.Body.String())
	}
	if rec := trade(code.Code); rec.Code != http.StatusUnauthorized {
		t.Errorf("the code worked twice: %d", rec.Code)
	}
	// The page's calls are same-origin and carry the session.
	if rec := send(srv, "GET", "http://localhost:7778/api/links?details=1", "", "Authorization", "Bearer "+sess.Session, "Sec-Fetch-Site", "same-origin"); rec.Code != http.StatusOK {
		t.Errorf("with the session: %d %s", rec.Code, rec.Body.String())
	}
	// A session can't mint codes, so it can't outlive itself.
	if rec := send(srv, "POST", "http://127.0.0.1:7778/api/manage-code", "{}", "Authorization", "Bearer "+sess.Session); rec.Code != http.StatusForbidden {
		t.Errorf("code from a session: %d, want 403", rec.Code)
	}
	if rec := send(srv, "POST", "http://127.0.0.1:7778/api/manage-code", "{}", "Authorization", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("code with no token: %d, want 401", rec.Code)
	}
}

func TestAPIListDetails(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.md")
	os.WriteFile(file, nil, 0o644)
	srv, _ := newTestServerFor(t, newSplitStore(t, "web = https://example.com\nfolder = "+dir+"\n", "a-md = "+file+" | https://sb/a\ngone-md = /nope/gone.md\n"))
	rec := get(srv, "http://127.0.0.1:7778/api/links?details=1", desktopUA)
	var l listing
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range l.Links {
		got = append(got, d.Slug+":"+d.Source+":"+d.Kind+":"+d.Phone)
	}
	want := "web:yours:url:,folder:yours:folder:,a-md:sl-link:file:https://sb/a,gone-md:sl-link:missing:"
	if l.Hub != "https://hub.example.ts.net" || strings.Join(got, ",") != want {
		t.Errorf("hub = %q, links = %v\nwant %s", l.Hub, got, want)
	}
}

func TestAPIRename(t *testing.T) {
	srv, _ := newTestServer(t, testLinks)
	if rec := send(srv, "POST", "http://127.0.0.1:7778/api/links/budget/rename", `{"to":"money"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get(srv, "http://hub.example.ts.net/money", desktopUA); rec.Header().Get("Location") != "http://localhost:7778/o/money" {
		t.Errorf("new slug: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := send(srv, "POST", "http://127.0.0.1:7778/api/links/money/rename", `{"to":"daily"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("rename onto a taken slug: %d, want 400", rec.Code)
	}
}

func TestOpenRefusesTargetsThatArentFullPaths(t *testing.T) {
	srv, opened := newTestServer(t, "rel = Documents/x.pdf\nflag = -a Calculator\nweb = https://example.com\nhome = ~\n")
	for _, slug := range []string{"rel", "flag", "web"} {
		if rec := get(srv, "http://localhost:7778/o/"+slug, desktopUA); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", slug, rec.Code)
		}
	}
	if rec := get(srv, "http://localhost:7778/o/home", desktopUA); rec.Code != http.StatusOK {
		t.Errorf("~: status = %d, want 200", rec.Code)
	}
	if strings.Join(*opened, ",") != "/Users/test" {
		t.Errorf("opened = %v, want only the home folder", *opened)
	}
}

func TestOpenShowsFilelocInFinder(t *testing.T) {
	srv, opened := newTestServer(t, "loc = ~/Desktop/thing.fileloc\n")
	get(srv, "http://localhost:7778/o/loc", desktopUA)
	if strings.Join(*opened, ",") != "reveal /Users/test/Desktop/thing.fileloc" {
		t.Errorf("opened = %v", *opened)
	}
}
