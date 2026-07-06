package main

import "testing"

func TestParseLinks(t *testing.T) {
	content := `
# comment
budget = ~/Documents/Finance/2026-budget.numbers
daily  = obsidian://daily?vault=Main

bad line without equals
UPPER = /tmp/x
empty-target =
o = /tmp/reserved
`
	links, warnings := ParseLinks(content)
	want := map[string]string{
		"budget": "~/Documents/Finance/2026-budget.numbers",
		"daily":  "obsidian://daily?vault=Main",
	}
	if len(links) != len(want) {
		t.Fatalf("got %d links (%v), want %d", len(links), links, len(want))
	}
	for slug, target := range want {
		if links[slug] != target {
			t.Errorf("links[%q] = %q, want %q", slug, links[slug], target)
		}
	}
	if len(warnings) != 4 {
		t.Errorf("got %d warnings (%v), want 4", len(warnings), warnings)
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
		"obsidian://daily?vault=Main":  true,
		"https://example.com/x":        true,
		"~/Documents/budget.numbers":   false,
		"/Users/alex/Desktop":          false,
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
