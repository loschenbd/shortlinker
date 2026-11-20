package main

import (
	"strings"
	"testing"
)

func TestParseEntries(t *testing.T) {
	content := `
# comment
budget = ~/Documents/Finance/2026-budget.numbers
daily  = obsidian://daily?vault=Main
notes-md = ~/notes.md | https://sb.example/notes

bad line without equals
UPPER = /tmp/x
empty-target =
o = /tmp/reserved
budget = /a/second/budget
`
	entries, warnings := parseEntries(content)
	var got []string
	for _, e := range entries {
		got = append(got, e.Slug+"="+e.Target+"|"+e.Phone)
	}
	want := "budget=~/Documents/Finance/2026-budget.numbers|,daily=obsidian://daily?vault=Main|,notes-md=~/notes.md|https://sb.example/notes"
	if strings.Join(got, ",") != want {
		t.Errorf("entries = %v\nwant %s", got, want)
	}
	if len(warnings) != 5 || !strings.Contains(warnings[4], `"budget" is already defined on line 3`) {
		t.Errorf("got %d warnings (%v), want 4 malformed lines and 1 repeat", len(warnings), warnings)
	}
}

func TestValidSlug(t *testing.T) {
	cases := map[string]bool{
		"budget":    true,
		"a-1":       true,
		"":          false,
		"UPPER":     false,
		"has space": false,
		"o":         false, // reserved
		"f":         false, // reserved
		"api":       false, // reserved
	}
	for slug, want := range cases {
		if got := ValidSlug(slug); got != want {
			t.Errorf("ValidSlug(%q) = %v, want %v", slug, got, want)
		}
	}
}

func TestIsRedirect(t *testing.T) {
	cases := map[string]bool{
		"obsidian://daily?vault=Main": true,
		"https://example.com/x":       true,
		"~/Documents/budget.numbers":  false,
		"/Users/alex/Desktop":         false,
	}
	for target, want := range cases {
		if got := IsRedirect(target); got != want {
			t.Errorf("IsRedirect(%q) = %v, want %v", target, got, want)
		}
	}
}

func TestExpandPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"~/Documents/x.pdf", "/Users/test/Documents/x.pdf"},
		{"~", "/Users/test"},
		{"/absolute/path", "/absolute/path"},
		{"https://example.com", "https://example.com"},
	}
	for _, c := range cases {
		if got := ExpandPath(c.in, "/Users/test"); got != c.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugFromPath(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/me/Documents/quarterly-report-2.pdf": "quarterly-report-2-pdf",
		"~/Notes/Q3 Plan (v2).md":                    "q3-plan-v2-md",
		"/Users/me/Code/my-app":                      "my-app",
		"/Users/me/.config":                          "config",
		"/Users/me/Café.md":                          "caf-md",
		"/":                                          "file",
		"/x/日本":                                      "file",
		"/x/" + strings.Repeat("a", 80) + ".md":      strings.Repeat("a", 60),
		"/x/" + strings.Repeat("a", 59) + " b.md":    strings.Repeat("a", 59),
	} {
		if got := SlugFromPath(in); got != want {
			t.Errorf("SlugFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}
