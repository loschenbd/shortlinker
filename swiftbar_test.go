package main

import (
	"strings"
	"testing"
)

func testMenu(state string, links ...linkDetails) menuInput {
	return menuInput{
		bin: "/Users/x/.local/bin/shortlinker", state: state, links: links,
		linksPath: "/Users/x/.config/shortlinker/links.txt", logPath: "/Users/x/Library/Logs/shortlinker.log",
		label: "com.x.shortlinker", uid: 501,
	}
}

func TestMenuShowsLinksAndActions(t *testing.T) {
	m := testMenu("up",
		linkDetails{Slug: "budget", Target: "~/b.numbers", Source: "yours", Kind: "file"},
		linkDetails{Slug: "readme-md", Target: "~/Code/a/README.md", Source: "sl-link", Kind: "file"},
		linkDetails{Slug: "gone-md", Target: "~/gone.md", Source: "sl-link", Kind: "missing"},
		linkDetails{Slug: "b-readme-md", Target: "~/Code/b/README.md", Source: "sl-link", Kind: "file"},
		linkDetails{Slug: "site", Target: "~/Sites/my site|v2", Source: "sl-link", Kind: "folder"},
	)
	got := renderMenu(m)
	for _, want := range []string{
		"| sfimage=link\n---\n5 links · 1 yours · 4 from sl-link\n---\n",
		"Link Finder selection | bash=/Users/x/.local/bin/shortlinker param1=menu param2=link param3=--selection param4=--copy param5=--notify terminal=false sfimage=link.badge.plus\n",
		"Find links… | bash=/Users/x/.local/bin/shortlinker param1=manage terminal=false sfimage=magnifyingglass\n",
		"Recently linked | sfimage=clock refresh=true\n",
		// newest first; the two README.md files say which folder; "|" can't break the line
		"--my site¦v2 | bash=/Users/x/.local/bin/shortlinker param1=copy param2=--notify param3=site terminal=false sfimage=folder\n",
		"--README.md · b | bash=/Users/x/.local/bin/shortlinker param1=copy param2=--notify param3=b-readme-md terminal=false sfimage=doc\n",
		"--README.md · b as Markdown | bash=/Users/x/.local/bin/shortlinker param1=copy param2=--markdown param3=--notify param4=b-readme-md terminal=false sfimage=text.quote alternate=true\n",
		"--README.md · a | ",
		"Prune 1 dead link | bash=/Users/x/.local/bin/shortlinker param1=prune param2=--notify terminal=false refresh=true sfimage=trash\n",
		"Edit links.txt | bash=/usr/bin/open param1=-t param2=/Users/x/.config/shortlinker/links.txt terminal=false sfimage=square.and.pencil\n",
		"---\nRestart shortlinker | bash=/bin/launchctl param1=kickstart param2=-k param3=gui/501/com.x.shortlinker terminal=false refresh=true sfimage=arrow.clockwise\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("menu is missing %q\n--- menu ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "gone.md |") || strings.Contains(got, "param3=budget") {
		t.Errorf("recent shows a missing file or one of your links:\n%s", got)
	}
	if strings.Index(got, "param3=site") > strings.Index(got, "param3=readme-md") {
		t.Errorf("recent isn't newest first:\n%s", got)
	}
}

func TestMenuNestedLeavesOutTheMenuBarAndServiceRows(t *testing.T) {
	m := testMenu("up", linkDetails{Slug: "a", Target: "/a", Source: "yours", Kind: "file"})
	m.nested = true
	got := renderMenu(m)
	if !strings.HasPrefix(got, "1 link · 1 yours · 0 from sl-link\n---\n") {
		t.Errorf("nested menu starts with %q", got)
	}
	for _, unwanted := range []string{"sfimage=link\n", "Restart", "Open log", "Prune"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("nested menu has %q:\n%s", unwanted, got)
		}
	}
	for _, want := range []string{"Recently linked | sfimage=clock refresh=true\n--Nothing linked yet\n", "No dead links | sfimage=trash\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("nested menu is missing %q:\n%s", want, got)
		}
	}
}

// SwiftBar 2.1.0-2.1.1 stops updating a submenu whose row count changes, so the top-level rows
// must be the same whether or not there are dead links or recent sl-link links.
func TestMenuKeepsItsShape(t *testing.T) {
	topLevel := func(m menuInput) (rows []string) {
		for _, l := range strings.Split(renderMenu(m), "\n") {
			if l != "" && (!strings.HasPrefix(l, "--") || l == "---") {
				rows = append(rows, strings.SplitN(l, " ", 2)[0])
			}
		}
		return rows
	}
	empty := testMenu("up", linkDetails{Slug: "a", Target: "/a", Source: "yours", Kind: "file"})
	busy := testMenu("up",
		linkDetails{Slug: "a", Target: "/a", Source: "yours", Kind: "file"},
		linkDetails{Slug: "b", Target: "/b", Source: "sl-link", Kind: "file"},
		linkDetails{Slug: "c", Target: "/c", Source: "sl-link", Kind: "missing"},
	)
	for _, nested := range []bool{false, true} {
		empty.nested, busy.nested = nested, nested
		a, b := topLevel(empty), topLevel(busy)
		if len(a) != len(b) {
			t.Errorf("nested=%v: %d top-level rows without dead or recent links, %d with them\n%q\n%q", nested, len(a), len(b), a, b)
		}
	}
}

func TestMenuWhenTheHubIsDownOrElsewhere(t *testing.T) {
	down := renderMenu(testMenu("down"))
	if !strings.HasPrefix(down, "| sfimage=exclamationmark.triangle\n---\nshortlinker isn't running\n---\nRestart shortlinker") {
		t.Errorf("down menu:\n%s", down)
	}
	if got := renderMenu(testMenu("not-hub")); !strings.Contains(got, "This Mac opens links; the hub Mac keeps them") || strings.Contains(got, "Link Finder selection") {
		t.Errorf("not-hub menu:\n%s", got)
	}
	m := testMenu("error")
	m.err = "hub refused: | --weird"
	if got := renderMenu(m); !strings.Contains(got, "\nhub refused: ¦ --weird\n") {
		t.Errorf("error menu:\n%s", got)
	}
}

func TestMenuQuote(t *testing.T) {
	for in, want := range map[string]string{"/a/b": "/a/b", "/My Files/x": `"/My Files/x"`, "": `""`, `say "hi"`: `"say 'hi'"`} {
		if got := menuQuote(in); got != want {
			t.Errorf("menuQuote(%q) = %s, want %s", in, got, want)
		}
	}
	if got := menuTitle("--x|y"); got != "x¦y" {
		t.Errorf("menuTitle = %q", got)
	}
}
