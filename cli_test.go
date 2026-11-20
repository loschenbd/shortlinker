package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListPrintsMatchingLinks(t *testing.T) {
	srv, _ := newTestServerFor(t, newSplitStore(t, "budget = ~/Documents/budget.numbers\ndaily = obsidian://daily\n", "q3-plan-md = /nope/Q3 Plan.md\n"))
	hub := httptest.NewServer(srv.LocalHandler())
	defer hub.Close()
	t.Setenv("SHORTLINKER_API", hub.URL)
	t.Setenv("SHORTLINKER_TOKEN", testToken)

	var out bytes.Buffer
	if code := runList(nil, &out); code != exitOK {
		t.Fatalf("ls: code %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "budget ") || !strings.Contains(lines[0], " kept") || !strings.HasSuffix(lines[2], " missing") {
		t.Errorf("ls =\n%s", out.String())
	}
	out.Reset()
	runList([]string{"PLAN", "q3"}, &out)
	if got := strings.TrimSpace(out.String()); !strings.HasPrefix(got, "q3-plan-md") || strings.Contains(got, "\n") {
		t.Errorf("ls PLAN q3 = %q, want just q3-plan-md", got)
	}

	t.Setenv("SHORTLINKER_TOKEN", "wrong")
	if code := runList(nil, &out); code != exitError {
		t.Errorf("wrong token: code %d, want %d", code, exitError)
	}
}

func TestMenuPathPutsToolFoldersFirst(t *testing.T) {
	if got := menuPath("/Users/x", ""); got != "/Users/x/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" {
		t.Errorf("menuPath = %q", got)
	}
}

func TestMarkdownLinkEscapesBrackets(t *testing.T) {
	if got := markdownLink("notes [draft].md", "https://hub/x"); got != `[notes \[draft\].md](https://hub/x)` {
		t.Errorf("markdownLink = %q", got)
	}
}
