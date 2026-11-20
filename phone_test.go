package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// The hub runs under launchd, whose PATH has none of the usual tool
// folders; sb-link lives in ~/.local/bin.
func TestHubFindsSbLinkWithLaunchdsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	if got := phoneBin("sb-link", home); got != "" {
		t.Fatalf("found %q before there was one", got)
	}
	os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755)
	bin := writeScript(t, filepath.Join(home, ".local", "bin"), "sb-link", "exit 0\n")
	if got := phoneBin("sb-link", home); got != bin {
		t.Errorf("phoneBin = %q, want %q", got, bin)
	}
	os.Chmod(bin, 0o644)
	if got := phoneBin("sb-link", home); got != "" {
		t.Errorf("phoneBin = %q for a file that can't run", got)
	}
}

// A Keychain prompt with nobody at the Mac would hold sb-link forever. The
// time limit ends it and whatever it started, and the link goes on without
// a page.
func TestPhonePageTimeLimitStopsWhatItStarted(t *testing.T) {
	old := phonePageTimeout
	phonePageTimeout = 2 * time.Second // macOS checks a new script before its first run
	defer func() { phonePageTimeout = old }()
	dir := t.TempDir()
	srv := NewServer(newTestStore(t, ""), "", "7778", dir)
	srv.phoneCmd = writeScript(t, dir, "sb-link", "sleep 30 &\necho $! > \""+dir+"/child\"\nwait\n")
	start := time.Now()
	_, err := srv.makePhonePage(context.Background(), filepath.Join(dir, "x.md"))
	if err == nil || !strings.Contains(err.Error(), "took longer") {
		t.Errorf("err = %v; want the time limit", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v; want about the time limit", d)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "child"))
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("no child pid: %q", data)
	}
	for i := 0; i < 40 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Error("what sb-link started outlived the time limit")
	}
}

// Two sb-links at once for files in one new project would both create its
// SilverBullet space.
func TestPhonePagesAreMadeOneAtATime(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(newTestStore(t, ""), "", "7778", dir)
	srv.phoneCmd = writeScript(t, dir, "sb-link", fmt.Sprintf(
		"mkdir %[1]q/running 2>/dev/null || echo overlap >> %[1]q/overlaps\nsleep 0.2\nrmdir %[1]q/running\necho https://sb.example/p\n", dir))
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := srv.makePhonePage(context.Background(), fmt.Sprintf("%s/%d.md", dir, i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if data, err := os.ReadFile(filepath.Join(dir, "overlaps")); err == nil {
		t.Errorf("sb-link ran %d times at once", strings.Count(string(data), "overlap")+1)
	}
}

func TestPhonePageAPI(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"notes.md", "plan.md", "main.go", "notes.pdf"} {
		os.WriteFile(filepath.Join(home, name), []byte("# x\n"), 0o644)
	}
	os.Mkdir(filepath.Join(home, "folder.md"), 0o755)
	srv := NewServer(newTestStore(t, "notes = ~/notes.md\nplan = ~/plan.md\ncode = ~/main.go\npdf = ~/notes.pdf\nfolder = ~/folder.md\n"+
		"gone = ~/gone.md\nweb = https://example.com/a.md\nhas = ~/plan.md | https://sb.example/existing\n"), "", "7778", home)
	srv.auth = newAPIAuth(testToken)
	runs := filepath.Join(home, "runs")
	srv.phoneCmd = writeScript(t, home, "sb-link", fmt.Sprintf(
		"echo \"$1\" >> %q\ncase \"$1\" in *plan.md) echo 'sb-link: login failed' >&2; exit 1;; esac\necho https://sb.example/notes\n", runs))
	local := httptest.NewServer(srv.LocalHandler())
	defer local.Close()
	hub := api{base: local.URL, token: testToken, client: local.Client()}

	reply, err := postPhonePage(hub, "notes")
	if err != nil || reply.Phone != "https://sb.example/notes" || reply.Label != "SilverBullet page" || reply.Reason != "" {
		t.Errorf("notes: %+v, %v; want its new page", reply, err)
	}
	if l, _, _ := srv.store.Lookup("notes"); l.Phone != "https://sb.example/notes" {
		t.Errorf("notes: saved phone = %q", l.Phone)
	}
	if reply, _ := postPhonePage(hub, "notes"); reply.Phone != "https://sb.example/notes" {
		t.Errorf("notes again: %+v", reply)
	}
	if reply, _ := postPhonePage(hub, "has"); reply.Phone != "https://sb.example/existing" {
		t.Errorf("has: %+v; want the page it has", reply)
	}
	reply, err = postPhonePage(hub, "plan")
	if err != nil || reply.Phone != "" || !strings.Contains(reply.Reason, "login failed") {
		t.Errorf("plan: %+v, %v; want sb-link's reason", reply, err)
	}
	if l, _, _ := srv.store.Lookup("plan"); l.Phone != "" {
		t.Errorf("plan: saved phone = %q after a failure", l.Phone)
	}
	if reply, err := postPhonePage(hub, "code"); err != nil || reply.Phone != "https://sb.example/notes" {
		t.Errorf("code: %+v, %v; want a page for a code file too", reply, err)
	}
	if data, _ := os.ReadFile(runs); strings.Count(string(data), "\n") != 3 {
		t.Errorf("sb-link ran for %q; want once each for notes, plan and code", data)
	}
	for slug, want := range map[string]string{"pdf": "Markdown or code", "folder": "Markdown or code", "gone": "Markdown or code", "web": "no file link", "nope": "no file link"} {
		if _, err := postPhonePage(hub, slug); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v; want %q", slug, err, want)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, local.URL+"/api/links/notes/phone-page", strings.NewReader("{}"))
	if resp, err := local.Client().Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %v %v; want 401", resp.StatusCode, err)
	}
}
